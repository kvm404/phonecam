// Package lifecycle implements the `phonecam status` and `phonecam stop`
// commands. Both read the session record written by `phonecam start`, verify
// the process is really alive (PID liveness plus a loopback control-server
// probe that guards against PID reuse), and then report or terminate it.
package lifecycle

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/session"
)

// stopSignalTimeout is the total time Stop waits for the process to exit after
// SIGTERM before giving up. pollInterval is how often it checks in between.
const (
	stopSignalTimeout = 5 * time.Second
	pollInterval      = 100 * time.Millisecond
)

// Store abstracts the persisted session record.
type Store interface {
	Read() (session.Record, error)
	Remove() error
}

// Process abstracts sending signals to a PID. Signal(pid, 0) is used as a
// liveness probe; SIGTERM is used to stop.
type Process interface {
	Signal(pid int, sig syscall.Signal) error
}

// HTTPDoer abstracts the HTTP client used to probe the control server.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// OSProcess implements Process against the real operating system.
type OSProcess struct{}

func (OSProcess) Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

// Manager runs the status and stop commands against injectable dependencies.
type Manager struct {
	store   Store
	process Process
	client  HTTPDoer
	now     func() time.Time
	sleep   func(time.Duration)
}

// NewManager returns a Manager. Nil dependencies default to the real OS: the
// XDG session store, syscall-based process signalling, and a 1s-timeout HTTP
// client.
func NewManager(store Store, process Process, client HTTPDoer) *Manager {
	if store == nil {
		store = session.NewStore(nil)
	}
	if process == nil {
		process = OSProcess{}
	}
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	return &Manager{
		store:   store,
		process: process,
		client:  client,
		now:     func() time.Time { return time.Now().UTC() },
		sleep:   time.Sleep,
	}
}

// state is the outcome of the shared liveness check.
type state int

const (
	stateNotRunning state = iota // no session file
	stateStale                   // session file present but process is gone/unrelated
	stateAlive                   // process is alive and its control server matches
)

type probeResult struct {
	approved       bool
	lastRTPms      *int64
	packetsDropped uint64
	packetsFwd     uint64
	hasRTP         bool
	trustedCount   int
}

// check performs the shared liveness check. When the process is dead or the
// control server does not match the recorded session, it removes the stale file
// and reports stateStale.
func (m *Manager) check() (session.Record, probeResult, state) {
	record, err := m.store.Read()
	if err != nil {
		return session.Record{}, probeResult{}, stateNotRunning
	}

	if !m.alive(record.PID) {
		_ = m.store.Remove()
		return record, probeResult{}, stateStale
	}

	probe, ok := m.probeControl(record)
	if !ok {
		_ = m.store.Remove()
		return record, probeResult{}, stateStale
	}

	return record, probe, stateAlive
}

// alive reports whether pid is a live process we can signal.
func (m *Manager) alive(pid int) bool {
	return m.process.Signal(pid, 0) == nil
}

// probeControl queries the control server's /status and confirms it reports the
// recorded session id. approved reflects the pairing state; ok is false when the
// server is unreachable, returns non-200, or reports a different session.
func (m *Manager) probeControl(record session.Record) (probeResult, bool) {
	url := fmt.Sprintf("http://127.0.0.1:%d/status", record.ControlPort)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return probeResult{}, false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return probeResult{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return probeResult{}, false
	}

	var body struct {
		Approved       bool    `json:"approved"`
		Session        string  `json:"session"`
		LastRTPms      *int64  `json:"last_rtp_ms"`
		PacketsDropped *uint64 `json:"packets_dropped_acl"`
		PacketsFwd     *uint64 `json:"packets_forwarded"`
		TrustedCount   int     `json:"trusted_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return probeResult{}, false
	}
	if body.Session != record.SessionID {
		return probeResult{}, false
	}
	probe := probeResult{approved: body.Approved, lastRTPms: body.LastRTPms, trustedCount: body.TrustedCount}
	if body.LastRTPms != nil {
		probe.hasRTP = true
	}
	if body.PacketsDropped != nil {
		probe.hasRTP = true
		probe.packetsDropped = *body.PacketsDropped
	}
	if body.PacketsFwd != nil {
		probe.hasRTP = true
		probe.packetsFwd = *body.PacketsFwd
	}
	return probe, true
}

// Status prints a status block and returns the process exit code.
func (m *Manager) Status(stdout, stderr io.Writer) int {
	record, probe, st := m.check()
	switch st {
	case stateNotRunning:
		fmt.Fprintln(stdout, "PhoneCam is not running.")
		return 1
	case stateStale:
		fmt.Fprintln(stdout, "Removed stale session file. PhoneCam is not running.")
		return 1
	}

	pairing := "Waiting for phone"
	if probe.approved {
		pairing = "Phone paired and streaming target active"
	} else if probe.trustedCount > 0 {
		pairing = "Waiting for phone (trusted reconnect allowed)"
	}
	uptime := m.now().Sub(record.StartedAt).Round(time.Second)

	fmt.Fprintln(stdout, "PhoneCam is running.")
	fmt.Fprintf(stdout, "  PID:            %d\n", record.PID)
	fmt.Fprintf(stdout, "  Uptime:         %s\n", uptime)
	fmt.Fprintf(stdout, "  Control port:   %d\n", record.ControlPort)
	fmt.Fprintf(stdout, "  RTP port:       %d\n", record.RTPPort)
	fmt.Fprintf(stdout, "  Virtual camera: %s\n", record.Device)
	fmt.Fprintf(stdout, "  Pairing:        %s\n", pairing)
	if probe.hasRTP {
		fmt.Fprintf(stdout, "  RTP:            %s\n", formatRTPLine(probe))
	}
	if probe.trustedCount > 0 {
		fmt.Fprintf(stdout, "  Trusted phones: %d\n", probe.trustedCount)
	}
	return 0
}

func formatRTPLine(probe probeResult) string {
	if probe.lastRTPms == nil {
		return fmt.Sprintf("silent, %d fwd, %d acl drops", probe.packetsFwd, probe.packetsDropped)
	}
	return fmt.Sprintf("live, last packet %dms ago, %d fwd, %d acl drops", *probe.lastRTPms, probe.packetsFwd, probe.packetsDropped)
}

// Stop sends SIGTERM to the running process, waits up to stopSignalTimeout for
// it to exit, and returns the process exit code. It never sends SIGKILL.
func (m *Manager) Stop(stdout, stderr io.Writer) int {
	record, _, st := m.check()
	switch st {
	case stateNotRunning:
		fmt.Fprintln(stdout, "PhoneCam is not running.")
		return 1
	case stateStale:
		fmt.Fprintln(stdout, "Removed stale session file. PhoneCam is not running.")
		return 1
	}

	// Best effort: the process may exit between the liveness check and now, in
	// which case the poll below still detects it and reports success.
	_ = m.process.Signal(record.PID, syscall.SIGTERM)

	attempts := int(stopSignalTimeout / pollInterval)
	for i := 0; i < attempts; i++ {
		if !m.alive(record.PID) {
			return m.reportStopped(stdout)
		}
		m.sleep(pollInterval)
	}
	if !m.alive(record.PID) {
		return m.reportStopped(stdout)
	}

	fmt.Fprintf(stderr, "PhoneCam did not stop within 5s (pid %d).\n", record.PID)
	return 1
}

// reportStopped removes any leftover session file (the start process removes its
// own on exit) and prints the stopped message.
func (m *Manager) reportStopped(stdout io.Writer) int {
	_ = m.store.Remove()
	fmt.Fprintln(stdout, "PhoneCam stopped.")
	return 0
}
