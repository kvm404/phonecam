package start

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/gstreamer"
	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
	"github.com/kvm404/phonecam/linux-cli/internal/session"
)

// fakeStore records the session records written and how many times Remove was
// called so tests can assert start persists and cleans up its session file.
type fakeStore struct {
	mu       sync.Mutex
	written  []session.Record
	removed  int
	writeErr error
}

func (s *fakeStore) Write(record session.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.written = append(s.written, record)
	return nil
}

func (s *fakeStore) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed++
	return nil
}

func (s *fakeStore) lastWritten() (session.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.written) == 0 {
		return session.Record{}, false
	}
	return s.written[len(s.written)-1], true
}

func (s *fakeStore) removeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removed
}

func init() {
	// Shorten the approval poll so tests that gate the receiver on approval do
	// not spend real time in the wait loop.
	approvalPollInterval = 5 * time.Millisecond
}

// waitForPayload polls the run output until the pairing payload block has been
// written and returns the parsed payload.
func waitForPayload(t *testing.T, out *lockedBuffer) pairing.Payload {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		if payload, ok := parsePayload(out.String()); ok {
			return payload
		}
		select {
		case <-deadline:
			t.Fatalf("pairing payload was not written:\n%s", out.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func parsePayload(output string) (pairing.Payload, bool) {
	const marker = "Pairing payload:\n"
	idx := strings.Index(output, marker)
	if idx < 0 {
		return pairing.Payload{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(output[idx+len(marker):]))
	var payload pairing.Payload
	if err := decoder.Decode(&payload); err != nil {
		return pairing.Payload{}, false
	}
	if payload.SessionID == "" || payload.Token == "" {
		return pairing.Payload{}, false
	}
	return payload, true
}

// controlBase returns the loopback base URL for the control server the payload
// advertises. The listener is bound to loopback in tests, so the loopback
// checks on /approve and /pairing are satisfied.
func controlBase(t *testing.T, payload pairing.Payload) string {
	t.Helper()

	parsed, err := url.Parse(payload.Control)
	if err != nil {
		t.Fatalf("parse control url %q: %v", payload.Control, err)
	}
	return "http://127.0.0.1:" + parsed.Port()
}

// pairViaHTTP consumes the pairing token as a phone would, optionally reporting
// a negotiated video profile. It does not approve the session.
func pairViaHTTP(t *testing.T, payload pairing.Payload, video *pairing.VideoProfile) {
	t.Helper()

	pairBody := map[string]any{
		"session":  payload.SessionID,
		"token":    payload.Token,
		"phone":    map[string]string{"id": "phone-1", "name": "Pixel"},
		"rtp_port": 50000,
		"ssrc":     1234,
	}
	if video != nil {
		pairBody["video"] = video
	}
	if code := postJSONHTTP(t, controlBase(t, payload)+"/pair", pairBody); code != http.StatusAccepted {
		t.Fatalf("expected pair 202, got %d", code)
	}
}

// approveSessionHTTP approves an already-paired session via the /approve
// endpoint, exercising the automation path.
func approveSessionHTTP(t *testing.T, payload pairing.Payload) {
	t.Helper()

	if code := postJSONHTTP(t, controlBase(t, payload)+"/approve", map[string]any{"session": payload.SessionID}); code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d", code)
	}
}

// approveViaHTTP performs the full /pair (optionally reporting video) and
// /approve handshake against the control server the payload advertises.
func approveViaHTTP(t *testing.T, payload pairing.Payload, video *pairing.VideoProfile) {
	t.Helper()

	pairViaHTTP(t, payload, video)
	approveSessionHTTP(t, payload)
}

// waitForOutput blocks until the run output contains the given substring.
func waitForOutput(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("output never contained %q:\n%s", want, out.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func postJSONHTTP(t *testing.T, target string, body any) int {
	t.Helper()

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	resp, err := http.Post(target, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("post %s: %v", target, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// waitForReceiver blocks until the blocking receiver has recorded its config.
func waitForReceiver(t *testing.T, r *blockingReceiver) gstreamer.Config {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		if config, got := r.receivedConfig(); got {
			return config
		}
		select {
		case <-deadline:
			t.Fatal("receiver did not start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

type fakeSystem struct {
	hostname   string
	interfaces []net.Interface
	addrs      map[string][]net.Addr
}

type lockedBuffer struct {
	mu      sync.Mutex
	builder strings.Builder
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.builder.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.builder.String()
}

func (f fakeSystem) Hostname() (string, error) {
	if f.hostname == "" {
		return "phonecam-test", nil
	}
	return f.hostname, nil
}

func (f fakeSystem) Interfaces() ([]net.Interface, error) {
	return f.interfaces, nil
}

func (f fakeSystem) InterfaceAddrs(iface net.Interface) ([]net.Addr, error) {
	return f.addrs[iface.Name], nil
}

func (f fakeSystem) Listen(network, address string) (net.Listener, error) {
	return net.Listen(network, "127.0.0.1:0")
}

func (f fakeSystem) ListenPacket(network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, "127.0.0.1:0")
}

// blockingReceiver records the config it was given and blocks until the
// context is cancelled, then returns ctx.Err().
type blockingReceiver struct {
	mu     sync.Mutex
	config gstreamer.Config
	got    bool
}

func (r *blockingReceiver) Run(ctx context.Context, config gstreamer.Config) error {
	r.mu.Lock()
	r.config = config
	r.got = true
	r.mu.Unlock()

	<-ctx.Done()
	return ctx.Err()
}

func (r *blockingReceiver) receivedConfig() (gstreamer.Config, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config, r.got
}

// resultReceiver returns a fixed error (which may be nil) immediately.
type resultReceiver struct {
	err error
}

func (r resultReceiver) Run(ctx context.Context, config gstreamer.Config) error {
	return r.err
}

// fakePreflight records the device it was asked to verify and returns a fixed
// error (nil by default, meaning the device check passes).
type fakePreflight struct {
	mu        sync.Mutex
	err       error
	gotDevice string
	called    bool
}

func (p *fakePreflight) Verify(device string) error {
	p.mu.Lock()
	p.gotDevice = device
	p.called = true
	p.mu.Unlock()
	return p.err
}

func (p *fakePreflight) device() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gotDevice, p.called
}

func TestRunPrintsPairingPayloadAndStopsOnContextCancel(t *testing.T) {
	system := fakeSystem{
		hostname: "test-laptop",
		interfaces: []net.Interface{
			{Name: "wlan0", Flags: net.FlagUp},
		},
		addrs: map[string][]net.Addr{
			"wlan0": []net.Addr{mustCIDR(t, "192.168.1.42/24")},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(system, receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50123,
		}, nil, &out)
	}()

	payload := waitForPayload(t, &out)
	_, publicPort, err := net.SplitHostPort(payload.RTP)
	if err != nil {
		t.Fatalf("parse advertised RTP %q: %v", payload.RTP, err)
	}
	if publicPort != "50123" {
		t.Fatalf("expected QR/session RTP port 50123, got %s", publicPort)
	}
	approveViaHTTP(t, payload, nil)

	config := waitForReceiver(t, receiver)
	if config.RTPPort == 50123 {
		t.Fatal("receiver must use the gate inner port, not the public QR port 50123")
	}
	if config.RTPPort <= 0 {
		t.Fatalf("expected allocated inner RTP port, got %d", config.RTPPort)
	}
	if config.Device != "/dev/video10" {
		t.Fatalf("expected receiver device /dev/video10, got %q", config.Device)
	}
	if config.Width != 1280 || config.Height != 720 || config.FPS != 30 {
		t.Fatalf("expected advertised dimensions 1280x720@30, got %dx%d@%d", config.Width, config.Height, config.FPS)
	}

	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	output := out.String()
	for _, expected := range []string{
		"PhoneCam",
		"Virtual camera: /dev/video10",
		"Control server: http://192.168.1.42:",
		"RTP endpoint: 192.168.1.42:",
		"Scan this QR code with the PhoneCam Android app:",
		"Pairing payload:",
		`"transport": "rtp-h264"`,
		`"width": 1280`,
		"Phone connected: receiving 1280x720@30 video",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func testSystem() fakeSystem {
	return fakeSystem{
		hostname: "test-laptop",
		interfaces: []net.Interface{
			{Name: "wlan0", Flags: net.FlagUp},
		},
		addrs: map[string][]net.Addr{
			"wlan0": []net.Addr{mustAddr("192.168.1.42/24")},
		},
	}
}

func mustAddr(value string) net.Addr {
	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		panic(err)
	}
	network.IP = ip
	return network
}

func TestRunDefaultsVirtualCameraAndPassesItToReceiver(t *testing.T) {
	tests := []struct {
		name          string
		virtualCamera string
		wantDevice    string
	}{
		{name: "default", virtualCamera: "", wantDevice: DefaultVirtualCamera},
		{name: "custom", virtualCamera: "/dev/video7", wantDevice: "/dev/video7"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			receiver := &blockingReceiver{}
			var out lockedBuffer
			done := make(chan error, 1)
			go func() {
				done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
					VirtualCamera: tc.virtualCamera,
				}, nil, &out)
			}()

			payload := waitForPayload(t, &out)
			approveViaHTTP(t, payload, nil)

			config := waitForReceiver(t, receiver)

			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context canceled, got %v", err)
			}

			if config.Device != tc.wantDevice {
				t.Fatalf("expected receiver device %q, got %q", tc.wantDevice, config.Device)
			}
			if config.RTPPort <= 0 {
				t.Fatalf("expected allocated inner RTP port, got %d", config.RTPPort)
			}
			_, advertised, err := net.SplitHostPort(payload.RTP)
			if err != nil {
				t.Fatalf("parse advertised RTP %q: %v", payload.RTP, err)
			}
			if advertised == strconv.Itoa(config.RTPPort) {
				t.Fatalf("receiver port %d must not equal advertised public RTP port", config.RTPPort)
			}
			if !strings.Contains(out.String(), "Virtual camera: "+tc.wantDevice) {
				t.Fatalf("expected output to name virtual camera %q, got:\n%s", tc.wantDevice, out.String())
			}
		})
	}
}

func TestRunReturnsReceiverError(t *testing.T) {
	receiver := resultReceiver{err: errors.New("boom")}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(context.Background(), Config{}, nil, &out)
	}()

	approveViaHTTP(t, waitForPayload(t, &out), nil)

	err := <-done
	if err == nil {
		t.Fatal("expected error from receiver failure")
	}
	if !strings.Contains(err.Error(), "gstreamer receiver") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error mentioning receiver failure, got %v", err)
	}
}

func TestRunReturnsErrorWhenReceiverExitsCleanly(t *testing.T) {
	receiver := resultReceiver{err: nil}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(context.Background(), Config{}, nil, &out)
	}()

	approveViaHTTP(t, waitForPayload(t, &out), nil)

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatalf("expected exited unexpectedly error, got %v", err)
	}
}

func TestRunStartsReceiverWithNegotiatedDimensions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50200,
		}, nil, &out)
	}()

	payload := waitForPayload(t, &out)
	approveViaHTTP(t, payload, &pairing.VideoProfile{Width: 720, Height: 1280, FPS: 24})

	config := waitForReceiver(t, receiver)
	if config.Width != 720 || config.Height != 1280 || config.FPS != 24 {
		t.Fatalf("expected negotiated dimensions 720x1280@24, got %dx%d@%d", config.Width, config.Height, config.FPS)
	}
	if !strings.Contains(out.String(), "Phone connected: receiving 720x1280@24 video") {
		t.Fatalf("expected phone-connected line, got:\n%s", out.String())
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunNeverStartsReceiverWhenCancelledBeforeApproval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, nil, &out)
	}()

	// Wait until the server is up and pairing has been advertised, but never
	// approve the session.
	waitForPayload(t, &out)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	if _, started := receiver.receivedConfig(); started {
		t.Fatal("expected receiver not to start before approval")
	}
	if strings.Contains(out.String(), "Phone connected") {
		t.Fatalf("expected no phone-connected line, got:\n%s", out.String())
	}
}

func TestRunFailsFastOnPreflightError(t *testing.T) {
	receiver := &blockingReceiver{}
	preflight := &fakePreflight{err: errors.New("virtual camera /dev/video10 does not exist")}
	var out lockedBuffer

	err := New(testSystem(), receiver, preflight, &fakeStore{}).Run(context.Background(), Config{
		VirtualCamera: "/dev/video10",
	}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected preflight error, got %v", err)
	}

	device, called := preflight.device()
	if !called {
		t.Fatal("expected preflight to be called")
	}
	if device != "/dev/video10" {
		t.Fatalf("expected preflight device /dev/video10, got %q", device)
	}

	if _, started := receiver.receivedConfig(); started {
		t.Fatal("expected receiver not to start when preflight fails")
	}

	if out.String() != "" {
		t.Fatalf("expected no output on preflight failure, got:\n%s", out.String())
	}
}

func TestRunPreflightReceivesDefaultedDevice(t *testing.T) {
	receiver := &blockingReceiver{}
	preflight := &fakePreflight{err: errors.New("boom")}
	var out lockedBuffer

	_ = New(testSystem(), receiver, preflight, &fakeStore{}).Run(context.Background(), Config{}, nil, &out)

	device, called := preflight.device()
	if !called {
		t.Fatal("expected preflight to be called")
	}
	if device != DefaultVirtualCamera {
		t.Fatalf("expected preflight to receive defaulted device %q, got %q", DefaultVirtualCamera, device)
	}
}

// listenErrSystem behaves like fakeSystem but fails to bind TCP listeners,
// simulating a port already in use.
type listenErrSystem struct {
	fakeSystem
}

func (listenErrSystem) Listen(network, address string) (net.Listener, error) {
	return nil, errors.New("address already in use")
}

func TestRunWrapsBusyControlPort(t *testing.T) {
	sys := listenErrSystem{fakeSystem: testSystem()}
	var out lockedBuffer
	err := New(sys, &blockingReceiver{}, &fakePreflight{}, &fakeStore{}).Run(context.Background(), Config{
		VirtualCamera: "/dev/video10",
		ControlPort:   DefaultControlPort,
	}, nil, &out)
	if err == nil {
		t.Fatal("expected error when control port is busy")
	}
	if !strings.Contains(err.Error(), "control port 47470 is busy") {
		t.Fatalf("expected busy control port message, got %v", err)
	}
	if !strings.Contains(err.Error(), "--control-port") {
		t.Fatalf("expected error to mention --control-port flag, got %v", err)
	}
}

func TestLocalIPv4FallsBackToLoopback(t *testing.T) {
	got, err := localIPv4(fakeSystem{})
	if err != nil {
		t.Fatalf("localIPv4 failed: %v", err)
	}
	if got != "127.0.0.1" {
		t.Fatalf("expected loopback fallback, got %q", got)
	}
}

func TestRunWritesSessionRecordAndRemovesOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	store := &fakeStore{}
	startedAt := time.Date(2026, 7, 16, 9, 30, 0, 0, time.UTC)
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, store).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50321,
			Now:           startedAt,
		}, nil, &out)
	}()

	payload := waitForPayload(t, &out)

	record, ok := store.lastWritten()
	if !ok {
		t.Fatal("expected a session record to be written")
	}
	if record.SessionID != payload.SessionID {
		t.Fatalf("expected recorded session id %q, got %q", payload.SessionID, record.SessionID)
	}
	if record.RTPPort != 50321 {
		t.Fatalf("expected recorded RTP port 50321, got %d", record.RTPPort)
	}
	if record.ControlPort <= 0 {
		t.Fatalf("expected a recorded control port, got %d", record.ControlPort)
	}
	if record.Device != "/dev/video10" {
		t.Fatalf("expected recorded device /dev/video10, got %q", record.Device)
	}
	if record.PID != os.Getpid() {
		t.Fatalf("expected recorded PID %d, got %d", os.Getpid(), record.PID)
	}
	if !record.StartedAt.Equal(startedAt) {
		t.Fatalf("expected recorded StartedAt %v, got %v", startedAt, record.StartedAt)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if store.removeCount() == 0 {
		t.Fatal("expected session record removed on exit")
	}
}

func TestRunRemovesSessionRecordOnReceiverFailure(t *testing.T) {
	store := &fakeStore{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), resultReceiver{err: errors.New("boom")}, &fakePreflight{}, store).Run(context.Background(), Config{}, nil, &out)
	}()

	approveViaHTTP(t, waitForPayload(t, &out), nil)

	if err := <-done; err == nil {
		t.Fatal("expected receiver failure error")
	}
	if store.removeCount() == 0 {
		t.Fatal("expected session record removed after receiver failure")
	}
}

func TestRunContinuesWhenSessionWriteFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	store := &fakeStore{writeErr: errors.New("disk full")}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, store).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, nil, &out)
	}()

	// Start still proceeds to receiver despite the write failure.
	approveViaHTTP(t, waitForPayload(t, &out), nil)
	waitForReceiver(t, receiver)

	if !strings.Contains(out.String(), "Warning: could not record session") {
		t.Fatalf("expected a warning on write failure, got:\n%s", out.String())
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunPromptApprovesOnStdinYes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50401,
		}, strings.NewReader("y\n"), &out)
	}()

	payload := waitForPayload(t, &out)
	pairViaHTTP(t, payload, &pairing.VideoProfile{Width: 640, Height: 480, FPS: 15})

	waitForOutput(t, &out, `Phone "Pixel" wants to connect. Approve? [y/N]`)

	config := waitForReceiver(t, receiver)
	if config.Width != 640 || config.Height != 480 || config.FPS != 15 {
		t.Fatalf("expected negotiated dimensions 640x480@15, got %dx%d@%d", config.Width, config.Height, config.FPS)
	}
	if !strings.Contains(out.String(), "Phone connected: receiving 640x480@15 video") {
		t.Fatalf("expected phone-connected line, got:\n%s", out.String())
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunPromptDeniesPairing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
	}{
		{name: "no", answer: "n\n"},
		{name: "empty", answer: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver := &blockingReceiver{}
			var out lockedBuffer
			done := make(chan error, 1)
			go func() {
				done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(context.Background(), Config{
					VirtualCamera: "/dev/video10",
				}, strings.NewReader(tc.answer), &out)
			}()

			payload := waitForPayload(t, &out)
			pairViaHTTP(t, payload, nil)

			if err := <-done; err != nil {
				t.Fatalf("expected nil error on denial, got %v", err)
			}
			if !strings.Contains(out.String(), "Pairing denied.") {
				t.Fatalf("expected pairing denied message, got:\n%s", out.String())
			}
			if _, started := receiver.receivedConfig(); started {
				t.Fatal("expected receiver not to start on denial")
			}
		})
	}
}

func TestRunAutoApproveSkipsPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50402,
			AutoApprove:   true,
		}, nil, &out)
	}()

	payload := waitForPayload(t, &out)
	pairViaHTTP(t, payload, nil)

	waitForReceiver(t, receiver)
	waitForOutput(t, &out, `Auto-approved phone "Pixel".`)
	if strings.Contains(out.String(), "wants to connect") {
		t.Fatalf("expected no prompt with auto-approve, got:\n%s", out.String())
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunHTTPApproveWithBlockingStdin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer

	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50403,
		}, pr, &out)
	}()

	payload := waitForPayload(t, &out)
	pairViaHTTP(t, payload, nil)
	waitForOutput(t, &out, `Phone "Pixel" wants to connect`)

	approveSessionHTTP(t, payload)

	waitForReceiver(t, receiver)
	waitForOutput(t, &out, "Approved via control API.")

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunStatusIncludesGateCounters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, nil, &out)
	}()

	payload := waitForPayload(t, &out)
	approveViaHTTP(t, payload, nil)
	waitForReceiver(t, receiver)

	resp, err := http.Get(controlBase(t, payload) + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if _, ok := status["packets_dropped_acl"]; !ok {
		t.Fatalf("expected packets_dropped_acl on /status, got %#v", status)
	}
	if _, ok := status["last_rtp_ms"]; !ok {
		t.Fatalf("expected last_rtp_ms on /status, got %#v", status)
	}
	if _, ok := status["resume_token"]; ok {
		t.Fatal("/status must not include secrets")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunRetriesReceiverBindError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &flakyBindReceiver{fails: 2}
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, nil, &out)
	}()

	approveViaHTTP(t, waitForPayload(t, &out), nil)
	waitForReceiverAttempts(t, receiver, 3)

	ports := receiver.portsCopy()
	if len(ports) < 3 {
		t.Fatalf("expected 3 launch attempts, got %v", ports)
	}
	if ports[0] == ports[1] || ports[1] == ports[2] {
		t.Fatalf("bind retries must not reuse a failed inner port: %v", ports)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunCancelWhilePromptPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	receiver := &blockingReceiver{}
	var out lockedBuffer

	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	done := make(chan error, 1)
	go func() {
		done <- New(testSystem(), receiver, &fakePreflight{}, &fakeStore{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, pr, &out)
	}()

	payload := waitForPayload(t, &out)
	pairViaHTTP(t, payload, nil)
	waitForOutput(t, &out, `Phone "Pixel" wants to connect`)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if _, started := receiver.receivedConfig(); started {
		t.Fatal("expected receiver not to start when cancelled at prompt")
	}
}

type flakyBindReceiver struct {
	mu       sync.Mutex
	fails    int
	attempts int
	ports    []int
}

func (r *flakyBindReceiver) Run(ctx context.Context, config gstreamer.Config) error {
	r.mu.Lock()
	r.attempts++
	r.ports = append(r.ports, config.RTPPort)
	fails := r.fails
	attempt := r.attempts
	r.mu.Unlock()

	if attempt <= fails {
		return errors.New("could not bind to address")
	}
	<-ctx.Done()
	return ctx.Err()
}

func (r *flakyBindReceiver) portsCopy() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.ports...)
}

func waitForReceiverAttempts(t *testing.T, r *flakyBindReceiver, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		got := r.attempts
		r.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("receiver attempts=%d, want %d", got, want)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func mustCIDR(t *testing.T, value string) net.Addr {
	t.Helper()

	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	network.IP = ip
	return network
}
