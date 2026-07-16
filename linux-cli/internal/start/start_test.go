package start

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/gstreamer"
	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
)

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

// approveViaHTTP performs the /pair (optionally reporting video) and /approve
// handshake against the control server the payload advertises. The listener is
// bound to loopback in tests, so /approve's loopback check is satisfied.
func approveViaHTTP(t *testing.T, payload pairing.Payload, video *pairing.VideoProfile) {
	t.Helper()

	parsed, err := url.Parse(payload.Control)
	if err != nil {
		t.Fatalf("parse control url %q: %v", payload.Control, err)
	}
	base := "http://127.0.0.1:" + parsed.Port()

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
	if code := postJSONHTTP(t, base+"/pair", pairBody); code != http.StatusAccepted {
		t.Fatalf("expected pair 202, got %d", code)
	}
	if code := postJSONHTTP(t, base+"/approve", map[string]any{"session": payload.SessionID}); code != http.StatusOK {
		t.Fatalf("expected approve 200, got %d", code)
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
		done <- New(system, receiver, &fakePreflight{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50123,
		}, &out)
	}()

	payload := waitForPayload(t, &out)
	approveViaHTTP(t, payload, nil)

	config := waitForReceiver(t, receiver)
	if config.RTPPort != 50123 {
		t.Fatalf("expected receiver RTP port 50123, got %d", config.RTPPort)
	}
	if config.Device != "/dev/video10" {
		t.Fatalf("expected receiver device /dev/video10, got %q", config.Device)
	}
	if config.Width != 1280 || config.Height != 720 || config.FPS != 30 {
		t.Fatalf("expected advertised dimensions 1280x720@30, got %dx%d@%d", config.Width, config.Height, config.FPS)
	}

	cancel()
	err := <-done
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
				done <- New(testSystem(), receiver, &fakePreflight{}).Run(ctx, Config{
					VirtualCamera: tc.virtualCamera,
				}, &out)
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
				t.Fatalf("expected allocated RTP port, got %d", config.RTPPort)
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
		done <- New(testSystem(), receiver, &fakePreflight{}).Run(context.Background(), Config{}, &out)
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
		done <- New(testSystem(), receiver, &fakePreflight{}).Run(context.Background(), Config{}, &out)
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
		done <- New(testSystem(), receiver, &fakePreflight{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50200,
		}, &out)
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
		done <- New(testSystem(), receiver, &fakePreflight{}).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
		}, &out)
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

	err := New(testSystem(), receiver, preflight).Run(context.Background(), Config{
		VirtualCamera: "/dev/video10",
	}, &out)
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

	_ = New(testSystem(), receiver, preflight).Run(context.Background(), Config{}, &out)

	device, called := preflight.device()
	if !called {
		t.Fatal("expected preflight to be called")
	}
	if device != DefaultVirtualCamera {
		t.Fatalf("expected preflight to receive defaulted device %q, got %q", DefaultVirtualCamera, device)
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

func mustCIDR(t *testing.T, value string) net.Addr {
	t.Helper()

	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	network.IP = ip
	return network
}
