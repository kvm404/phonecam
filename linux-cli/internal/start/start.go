package start

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/control"
	"github.com/kvm404/phonecam/linux-cli/internal/gstreamer"
	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
	"github.com/kvm404/phonecam/linux-cli/internal/qrcode"
	"github.com/kvm404/phonecam/linux-cli/internal/rtp"
	"github.com/kvm404/phonecam/linux-cli/internal/session"
	"github.com/kvm404/phonecam/linux-cli/internal/v4l2"
)

const DefaultVirtualCamera = "/dev/video10"

// DefaultControlPort and DefaultRTPPort are the fixed ports PhoneCam binds by
// default so users can write static firewall allow rules. A value of 0 in
// Config restores the previous ephemeral/random-port behavior.
const (
	DefaultControlPort = 47470
	DefaultRTPPort     = 47471
)

// approvalPollInterval is how often the run loop checks whether the pairing
// session has been approved before starting the receiver. It is a package-level
// var so tests can shorten it.
var approvalPollInterval = 200 * time.Millisecond

const (
	receiverBindRetryWindow = 500 * time.Millisecond
	receiverBindAttempts    = 5
)

type runtimeMedia struct {
	gate *rtp.Gate
}

func (m *runtimeMedia) SetAllow(src pairing.RTPSource) {
	m.gate.SetAllow(src)
}

func (m *runtimeMedia) Stats() rtp.Stats {
	return m.gate.Stats()
}

func (m *runtimeMedia) RestartReceiver(pairing.VideoProfile) error {
	return nil
}

type System interface {
	Hostname() (string, error)
	Interfaces() ([]net.Interface, error)
	InterfaceAddrs(net.Interface) ([]net.Addr, error)
	Listen(network, address string) (net.Listener, error)
	ListenPacket(network, address string) (net.PacketConn, error)
}

type Receiver interface {
	Run(ctx context.Context, config gstreamer.Config) error
}

// Preflight verifies the virtual camera device before the receiver is started.
type Preflight interface {
	Verify(device string) error
}

// SessionStore persists a record of the running start process so `phonecam
// status` and `phonecam stop` can find it from another terminal.
type SessionStore interface {
	Write(session.Record) error
	Remove() error
}

// v4l2Preflight adapts v4l2.Verify to the Preflight interface using OSSystem.
type v4l2Preflight struct{}

func (v4l2Preflight) Verify(device string) error {
	return v4l2.Verify(v4l2.OSSystem{}, device)
}

type OSSystem struct{}

func (OSSystem) Hostname() (string, error) {
	return os.Hostname()
}

func (OSSystem) Interfaces() ([]net.Interface, error) {
	return net.Interfaces()
}

func (OSSystem) InterfaceAddrs(iface net.Interface) ([]net.Addr, error) {
	return iface.Addrs()
}

func (OSSystem) Listen(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}

func (OSSystem) ListenPacket(network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, address)
}

type Config struct {
	VirtualCamera string
	ControlPort   int
	RTPPort       int
	Now           time.Time
	// AutoApprove approves the first phone to pair without prompting. It is
	// intended for automation and headless use.
	AutoApprove bool
}

type Runtime struct {
	system    System
	receiver  Receiver
	preflight Preflight
	store     SessionStore
}

func New(system System, receiver Receiver, preflight Preflight, store SessionStore) Runtime {
	if system == nil {
		system = OSSystem{}
	}
	if receiver == nil {
		receiver = gstreamer.NewRunner(nil)
	}
	if preflight == nil {
		preflight = v4l2Preflight{}
	}
	if store == nil {
		store = session.NewStore(nil)
	}
	return Runtime{system: system, receiver: receiver, preflight: preflight, store: store}
}

func (r Runtime) Run(ctx context.Context, config Config, stdin io.Reader, stdout io.Writer) error {
	recvCtx, recvCancel := context.WithCancel(ctx)
	defer recvCancel()

	virtualCamera := config.VirtualCamera
	if virtualCamera == "" {
		virtualCamera = DefaultVirtualCamera
	}

	if err := r.preflight.Verify(virtualCamera); err != nil {
		return err
	}

	host, err := localIPv4(r.system)
	if err != nil {
		return err
	}

	controlListener, err := listenTCP(r.system, config.ControlPort)
	if err != nil {
		if config.ControlPort > 0 {
			return fmt.Errorf("control port %d is busy (use --control-port to change): %w", config.ControlPort, err)
		}
		return err
	}
	defer controlListener.Close()

	gate, err := rtp.NewGate(config.RTPPort)
	if err != nil {
		if config.RTPPort > 0 {
			return fmt.Errorf("rtp port %d is busy (use --rtp-port to change): %w", config.RTPPort, err)
		}
		return err
	}
	defer gate.Close()
	rtpPort := gate.PublicRTPPort()

	hostname, err := r.system.Hostname()
	if err != nil || hostname == "" {
		hostname = "phonecam-linux"
	}

	controlPort := controlListener.Addr().(*net.TCPAddr).Port
	now := config.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	pairSession, err := pairing.New(pairing.Config{
		LaptopName: hostname,
		ControlURL: fmt.Sprintf("http://%s:%d", host, controlPort),
		RTPHost:    host,
		RTPPort:    rtpPort,
		Now:        now,
	})
	if err != nil {
		return err
	}

	// The control listener is bound and the pairing session exists, so status
	// and stop have everything they need to find and verify this process. A
	// write failure is non-fatal: start continues, status/stop just won't find
	// it.
	record := session.Record{
		PID:         os.Getpid(),
		ControlPort: controlPort,
		RTPPort:     rtpPort,
		SessionID:   pairSession.Payload().SessionID,
		Device:      virtualCamera,
		StartedAt:   now,
	}
	if err := r.store.Write(record); err != nil {
		fmt.Fprintf(stdout, "Warning: could not record session for status/stop: %v\n", err)
	}
	defer r.store.Remove()

	server := &http.Server{
		Handler: control.New(control.Config{
			Session: pairSession,
			Media:   &runtimeMedia{gate: gate},
		}).Handler(),
	}
	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(controlListener); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	gateErr := make(chan error, 1)
	go func() {
		gateErr <- gate.Run(recvCtx)
	}()

	writeStartOutput(stdout, virtualCamera, pairSession)

	shutdownServer := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}

	// Do not start the receiver until the phone has been approved. The phone
	// reports its actual negotiated video profile during pairing, which we use
	// to size the receiver pipeline. Approval can arrive three ways: an
	// interactive y/N answer on stdin, the HTTP /approve endpoint (used by
	// automation), or automatically when AutoApprove is set.
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()

	answerCh := make(chan string, 1)
	var promptHandled bool

	for !pairSession.IsApproved() {
		// When the phone first consumes its token it becomes a pending phone
		// awaiting approval. Handle that transition exactly once.
		if !promptHandled {
			if phone, ok := pairSession.PendingPhone(); ok {
				promptHandled = true
				if config.AutoApprove {
					if err := pairSession.Approve(time.Now().UTC()); err != nil {
						fmt.Fprintln(stdout, err)
						shutdownServer()
						pairSession.Invalidate()
						return err
					}
					fmt.Fprintf(stdout, "Auto-approved phone %q.\n", phone.Name)
					continue
				}
				fmt.Fprintf(stdout, "Phone %q wants to connect. Approve? [y/N] ", phone.Name)
				if stdin != nil {
					go func() {
						line, _ := bufio.NewReader(stdin).ReadString('\n')
						// answerCh is buffered (size 1) so this send never
						// blocks even if Run has already returned, letting the
						// goroutine exit instead of leaking.
						answerCh <- line
					}()
				}
			}
		}

		select {
		case <-ctx.Done():
			shutdownServer()
			pairSession.Invalidate()
			return ctx.Err()
		case err := <-errCh:
			pairSession.Invalidate()
			return err
		case err := <-gateErr:
			shutdownServer()
			pairSession.Invalidate()
			return gateRunError(ctx, err)
		case answer := <-answerCh:
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "y", "yes":
				if err := pairSession.Approve(time.Now().UTC()); err != nil {
					fmt.Fprintln(stdout, err)
					shutdownServer()
					pairSession.Invalidate()
					return err
				}
				// Approved: the loop condition now exits and start proceeds.
			default:
				fmt.Fprintln(stdout, "Pairing denied.")
				pairSession.Invalidate()
				shutdownServer()
				return nil
			}
		case <-ticker.C:
			// External approval via HTTP /approve while the prompt is pending.
			// The prompt line has no trailing newline, so lead with one.
			if pairSession.IsApproved() {
				fmt.Fprintf(stdout, "\nApproved via control API.\n")
			}
		}
	}

	src, ok := pairSession.ApprovedSource()
	if !ok {
		shutdownServer()
		pairSession.Invalidate()
		return fmt.Errorf("approved session has no RTP source")
	}
	if err := pairSession.BindRTPSource(src); err != nil {
		shutdownServer()
		pairSession.Invalidate()
		return err
	}
	gate.SetAllow(src)

	video := pairSession.NegotiatedVideo()
	fmt.Fprintf(stdout, "Phone connected: receiving %dx%d@%d video\n", video.Width, video.Height, video.FPS)

	receiverErr := make(chan error, 1)
	go func() {
		receiverErr <- r.runReceiver(recvCtx, gate, video, virtualCamera)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		pairSession.Invalidate()
		return ctx.Err()
	case err := <-errCh:
		pairSession.Invalidate()
		return err
	case err := <-gateErr:
		shutdownServer()
		pairSession.Invalidate()
		return gateRunError(ctx, err)
	case err := <-receiverErr:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		pairSession.Invalidate()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return fmt.Errorf("gstreamer receiver exited unexpectedly")
		}
		if strings.HasPrefix(err.Error(), "gstreamer receiver") {
			return err
		}
		return fmt.Errorf("gstreamer receiver: %w", err)
	}
}

func (r Runtime) runReceiver(ctx context.Context, gate *rtp.Gate, video pairing.VideoProfile, device string) error {
	var lastErr error
	for attempt := 0; attempt < receiverBindAttempts; attempt++ {
		if attempt > 0 {
			if err := gate.RefreshLocalPort(); err != nil {
				return err
			}
		}
		started := time.Now()
		err := r.receiver.Run(ctx, gstreamer.Config{
			RTPPort: gate.LocalRTPPort(),
			Device:  device,
			Width:   video.Width,
			Height:  video.Height,
			FPS:     video.FPS,
		})
		if err == nil || ctx.Err() != nil || !isBindError(err) || time.Since(started) >= receiverBindRetryWindow {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func isBindError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "bind") || strings.Contains(msg, "address already in use") || strings.Contains(msg, "eaddrinuse")
}

func gateRunError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return fmt.Errorf("rtp gate exited unexpectedly")
	}
	return fmt.Errorf("rtp gate: %w", err)
}

func writeStartOutput(w io.Writer, virtualCamera string, pairSession *pairing.Session) {
	payload := pairSession.Payload()
	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		payloadJSON = []byte("{}")
	}
	compactPayloadJSON, err := json.Marshal(payload)
	if err != nil {
		compactPayloadJSON = []byte("{}")
	}

	fmt.Fprintln(w, "PhoneCam")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Virtual camera: %s\n", virtualCamera)
	fmt.Fprintln(w, "Status: Waiting for phone")
	fmt.Fprintf(w, "Control server: %s\n", payload.Control)
	fmt.Fprintf(w, "RTP endpoint: %s\n", payload.RTP)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Scan this QR code with the PhoneCam Android app:")
	renderedQR, err := qrcode.RenderTerminal(string(compactPayloadJSON))
	if err != nil {
		fmt.Fprintf(w, "QR unavailable: %v\n", err)
	} else {
		fmt.Fprintln(w, renderedQR)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Pairing payload:")
	fmt.Fprintln(w, string(payloadJSON))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Once connected, select \"PhoneCam\" in your meeting app.")
}

func listenTCP(system System, port int) (net.Listener, error) {
	address := "0.0.0.0:0"
	if port > 0 {
		address = fmt.Sprintf("0.0.0.0:%d", port)
	}
	return system.Listen("tcp", address)
}

func localIPv4(system System) (string, error) {
	interfaces, err := system.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := system.InterfaceAddrs(iface)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := addrIP(addr)
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				return ipv4.String(), nil
			}
		}
	}

	return "127.0.0.1", nil
}

func addrIP(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}
