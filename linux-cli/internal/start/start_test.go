package start

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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
	var out lockedBuffer
	done := make(chan error, 1)
	go func() {
		done <- New(system).Run(ctx, Config{
			VirtualCamera: "/dev/video10",
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
