package lifecycle

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/session"
)

type fakeStore struct {
	record  session.Record
	readErr error
	removed int
}

func (s *fakeStore) Read() (session.Record, error) {
	if s.readErr != nil {
		return session.Record{}, s.readErr
	}
	return s.record, nil
}

func (s *fakeStore) Remove() error {
	s.removed++
	return nil
}

// fakeProcess records the signals it receives. aliveFunc, when set, decides the
// result of each liveness probe (Signal with sig 0) by call number so tests can
// model a process that exits mid-poll.
type fakeProcess struct {
	aliveErr  error
	killErr   error
	aliveFunc func(call int) error

	liveCalls int
	signals   []syscall.Signal
}

func (p *fakeProcess) Signal(pid int, sig syscall.Signal) error {
	p.signals = append(p.signals, sig)
	if sig == syscall.SIGTERM {
		return p.killErr
	}
	p.liveCalls++
	if p.aliveFunc != nil {
		return p.aliveFunc(p.liveCalls)
	}
	return p.aliveErr
}

func (p *fakeProcess) sentSignal(sig syscall.Signal) bool {
	for _, s := range p.signals {
		if s == sig {
			return true
		}
	}
	return false
}

// statusServer starts an httptest server that answers /status with the given
// session id and approval flag, and returns its listening port.
func statusServer(t *testing.T, sessionID string, approved bool) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body := `{"ok":true,"approved":` + strconv.FormatBool(approved) + `,"session":"` + sessionID + `"}`
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return srv, port
}

func newTestManager(store Store, process Process, client HTTPDoer) *Manager {
	m := NewManager(store, process, client)
	m.now = func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }
	m.sleep = func(time.Duration) {}
	return m
}

func TestStatusNotRunning(t *testing.T) {
	store := &fakeStore{readErr: session.ErrNoSession}
	process := &fakeProcess{}
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Status(&stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "PhoneCam is not running.") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if len(process.signals) != 0 {
		t.Fatalf("expected no signals when no session, got %v", process.signals)
	}
}

func TestStatusStaleDeadPID(t *testing.T) {
	store := &fakeStore{record: session.Record{PID: 999, ControlPort: 47470, SessionID: "s1"}}
	process := &fakeProcess{aliveErr: syscall.ESRCH}
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Status(&stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Removed stale session file. PhoneCam is not running.") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if store.removed != 1 {
		t.Fatalf("expected stale file removed once, got %d", store.removed)
	}
}

func TestStatusStaleHTTPMismatch(t *testing.T) {
	_, port := statusServer(t, "other-session", true)
	store := &fakeStore{record: session.Record{PID: 999, ControlPort: port, SessionID: "s1"}}
	process := &fakeProcess{} // alive
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Status(&stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Removed stale session file") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if store.removed != 1 {
		t.Fatalf("expected stale file removed, got %d", store.removed)
	}
}

func TestStatusRunningApproved(t *testing.T) {
	_, port := statusServer(t, "s1", true)
	record := session.Record{
		PID:         1234,
		ControlPort: port,
		RTPPort:     47471,
		SessionID:   "s1",
		Device:      "/dev/video10",
		StartedAt:   time.Date(2026, 7, 16, 11, 58, 30, 0, time.UTC),
	}
	m := newTestManager(&fakeStore{record: record}, &fakeProcess{}, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Status(&stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"PhoneCam is running.",
		"PID:            1234",
		"Uptime:         1m30s",
		"Control port:   " + strconv.Itoa(port),
		"RTP port:       47471",
		"Virtual camera: /dev/video10",
		"Pairing:        Phone paired and streaming target active",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestStatusPrintsRTPCounters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"approved":true,"session":"s1","last_rtp_ms":14,"packets_forwarded":390,"packets_dropped_acl":412}`))
	}))
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	record := session.Record{PID: 1234, ControlPort: port, SessionID: "s1"}
	m := newTestManager(&fakeStore{record: record}, &fakeProcess{}, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	if code := m.Status(&stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "RTP:            live, last packet 14ms ago, 390 fwd, 412 acl drops") {
		t.Fatalf("expected RTP counters, got:\n%s", out)
	}
}

func TestStatusPrintsSilentRTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"approved":true,"session":"s1","packets_dropped_acl":412,"packets_forwarded":0}`))
	}))
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	record := session.Record{PID: 1234, ControlPort: port, SessionID: "s1"}
	m := newTestManager(&fakeStore{record: record}, &fakeProcess{}, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	if code := m.Status(&stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "RTP:            silent, 0 fwd, 412 acl drops") {
		t.Fatalf("expected silent RTP line, got:\n%s", stdout.String())
	}
}

func TestStatusRunningWaiting(t *testing.T) {
	_, port := statusServer(t, "s1", false)
	record := session.Record{PID: 1234, ControlPort: port, SessionID: "s1", StartedAt: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(&fakeStore{record: record}, &fakeProcess{}, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	if code := m.Status(&stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Pairing:        Waiting for phone") {
		t.Fatalf("expected waiting pairing state, got:\n%s", stdout.String())
	}
}

func TestStatusWaitingTrustedReconnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"approved":false,"session":"s1","trusted_count":2}`))
	}))
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	record := session.Record{PID: 1234, ControlPort: port, SessionID: "s1"}
	m := newTestManager(&fakeStore{record: record}, &fakeProcess{}, http.DefaultClient)
	var stdout, stderr bytes.Buffer
	if code := m.Status(&stdout, &stderr); code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Waiting for phone (trusted reconnect allowed)") {
		t.Fatalf("expected trusted waiting line, got:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Trusted phones: 2") {
		t.Fatalf("expected trusted count, got:\n%s", stdout.String())
	}
}

func TestStopSuccess(t *testing.T) {
	_, port := statusServer(t, "s1", true)
	store := &fakeStore{record: session.Record{PID: 1234, ControlPort: port, SessionID: "s1"}}
	// Alive for the initial liveness check (call 1); dead afterwards.
	process := &fakeProcess{aliveFunc: func(call int) error {
		if call == 1 {
			return nil
		}
		return syscall.ESRCH
	}}
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Stop(&stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "PhoneCam stopped.") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if !process.sentSignal(syscall.SIGTERM) {
		t.Fatalf("expected SIGTERM to be sent, signals: %v", process.signals)
	}
	if process.sentSignal(syscall.SIGKILL) {
		t.Fatal("must never send SIGKILL")
	}
	if store.removed != 1 {
		t.Fatalf("expected leftover file removed once, got %d", store.removed)
	}
}

func TestStopTimeout(t *testing.T) {
	_, port := statusServer(t, "s1", true)
	store := &fakeStore{record: session.Record{PID: 1234, ControlPort: port, SessionID: "s1"}}
	process := &fakeProcess{} // always alive
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	code := m.Stop(&stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "PhoneCam did not stop within 5s (pid 1234).") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if process.sentSignal(syscall.SIGKILL) {
		t.Fatal("must never send SIGKILL")
	}
}

func TestStopNotRunning(t *testing.T) {
	store := &fakeStore{readErr: session.ErrNoSession}
	m := newTestManager(store, &fakeProcess{}, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	if code := m.Stop(&stdout, &stderr); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "PhoneCam is not running.") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestStopStaleDeadPID(t *testing.T) {
	store := &fakeStore{record: session.Record{PID: 999, ControlPort: 47470, SessionID: "s1"}}
	process := &fakeProcess{aliveErr: syscall.ESRCH}
	m := newTestManager(store, process, http.DefaultClient)

	var stdout, stderr bytes.Buffer
	if code := m.Stop(&stdout, &stderr); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Removed stale session file") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
	if process.sentSignal(syscall.SIGTERM) {
		t.Fatal("must not SIGTERM a dead/stale process")
	}
}

// ensure the default client path compiles against a doer error, exercising the
// unreachable-server branch of probeControl.
func TestProbeControlUnreachableIsStale(t *testing.T) {
	store := &fakeStore{record: session.Record{PID: 1234, ControlPort: 1, SessionID: "s1"}}
	process := &fakeProcess{}
	m := newTestManager(store, process, errDoer{errors.New("connection refused")})

	var stdout, stderr bytes.Buffer
	if code := m.Status(&stdout, &stderr); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Removed stale session file") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

type errDoer struct{ err error }

func (d errDoer) Do(*http.Request) (*http.Response, error) { return nil, d.err }
