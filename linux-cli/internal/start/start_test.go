package start

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/gstreamer"
)

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
		done <- New(system, receiver).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
			RTPPort:       50123,
			Now:           time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		}, &out)
	}()

	deadline := time.After(2 * time.Second)
	for !strings.Contains(out.String(), "Status: Waiting for phone") {
		select {
		case <-deadline:
			t.Fatalf("start output was not written:\n%s", out.String())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	config, got := receiver.receivedConfig()
	if !got {
		t.Fatal("expected receiver to be started")
	}
	if config.RTPPort != 50123 {
		t.Fatalf("expected receiver RTP port 50123, got %d", config.RTPPort)
	}
	if config.Device != "/dev/video10" {
		t.Fatalf("expected receiver device /dev/video10, got %q", config.Device)
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
				done <- New(testSystem(), receiver).Run(ctx, Config{
					VirtualCamera: tc.virtualCamera,
				}, &out)
			}()

			deadline := time.After(2 * time.Second)
			for !strings.Contains(out.String(), "Status: Waiting for phone") {
				select {
				case <-deadline:
					cancel()
					<-done
					t.Fatalf("start output was not written:\n%s", out.String())
				default:
					time.Sleep(10 * time.Millisecond)
				}
			}

			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context canceled, got %v", err)
			}

			config, got := receiver.receivedConfig()
			if !got {
				t.Fatal("expected receiver to be started")
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

	err := New(testSystem(), receiver).Run(context.Background(), Config{}, &out)
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

	err := New(testSystem(), receiver).Run(context.Background(), Config{}, &out)
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") {
		t.Fatalf("expected exited unexpectedly error, got %v", err)
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
