package smoke

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastRetries lowers retryInterval for the duration of a test and restores it
// afterwards. Tests run sequentially, so this is race-free.
func fastRetries(t *testing.T) {
	t.Helper()
	prev := retryInterval
	retryInterval = 2 * time.Millisecond
	t.Cleanup(func() { retryInterval = prev })
}

type fakePreflight struct {
	err       error
	gotDevice string
}

func (p *fakePreflight) Verify(device string) error {
	p.gotDevice = device
	return p.err
}

// blockingReceiver blocks until its context is cancelled, then returns ctx.Err.
type blockingReceiver struct{}

func (blockingReceiver) RunReceiver(ctx context.Context, port int, device string) error {
	<-ctx.Done()
	return ctx.Err()
}

type failingReceiver struct{ err error }

func (f failingReceiver) RunReceiver(ctx context.Context, port int, device string) error {
	return f.err
}

type blockingSender struct{}

func (blockingSender) RunSender(ctx context.Context, port int, encoder string) error {
	<-ctx.Done()
	return ctx.Err()
}

type recordingSender struct {
	mu      sync.Mutex
	encoder string
}

func (s *recordingSender) RunSender(ctx context.Context, port int, encoder string) error {
	s.mu.Lock()
	s.encoder = encoder
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (s *recordingSender) usedEncoder() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder
}

type failingSender struct{ err error }

func (f failingSender) RunSender(ctx context.Context, port int, encoder string) error {
	return f.err
}

// fakeReadback succeeds once its attempt count reaches succeedAfter. When
// succeedAfter is 0 it never succeeds and always reports a "no frames" error.
type fakeReadback struct {
	mu           sync.Mutex
	attempts     int
	succeedAfter int
}

func (f *fakeReadback) RunReadback(ctx context.Context, device string, frames int) error {
	f.mu.Lock()
	f.attempts++
	n := f.attempts
	f.mu.Unlock()

	if f.succeedAfter > 0 && n >= f.succeedAfter {
		return nil
	}
	return errors.New("no frames yet")
}

func (f *fakeReadback) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

type fakeProber struct {
	present map[string]bool
}

func (p fakeProber) HasElement(element string) bool {
	return p.present[element]
}

func x264Prober() fakeProber {
	return fakeProber{present: map[string]bool{"x264enc": true}}
}

func allocPort(network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, "127.0.0.1:0")
}

func testConfig() Config {
	return Config{Device: "/dev/video10", Frames: 5, Timeout: 2 * time.Second}
}

func TestRunPasses(t *testing.T) {
	fastRetries(t)

	sender := &recordingSender{}
	readback := &fakeReadback{succeedAfter: 1}
	var out bytes.Buffer

	err := New(&fakePreflight{}, blockingReceiver{}, sender, readback, x264Prober(), allocPort).
		Run(context.Background(), testConfig(), &out)
	if err != nil {
		t.Fatalf("expected pass, got %v", err)
	}

	output := out.String()
	for _, expected := range []string{
		"Using H.264 encoder: x264enc",
		"Starting receiver on 127.0.0.1:",
		"Starting test sender to 127.0.0.1:",
		"Smoke test passed: 5 frames reached /dev/video10",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
	if sender.usedEncoder() != "x264enc" {
		t.Fatalf("expected sender to use x264enc, got %q", sender.usedEncoder())
	}
}

func TestRunSucceedsAfterRetries(t *testing.T) {
	fastRetries(t)

	readback := &fakeReadback{succeedAfter: 3}
	var out bytes.Buffer

	err := New(&fakePreflight{}, blockingReceiver{}, blockingSender{}, readback, x264Prober(), allocPort).
		Run(context.Background(), testConfig(), &out)
	if err != nil {
		t.Fatalf("expected pass after retries, got %v", err)
	}
	if got := readback.attemptCount(); got < 3 {
		t.Fatalf("expected at least 3 readback attempts, got %d", got)
	}
}

func TestRunFailsWhenReceiverExits(t *testing.T) {
	fastRetries(t)

	readback := &fakeReadback{} // never succeeds
	var out bytes.Buffer

	err := New(&fakePreflight{}, failingReceiver{err: errors.New("boom")}, blockingSender{}, readback, x264Prober(), allocPort).
		Run(context.Background(), testConfig(), &out)
	if err == nil {
		t.Fatal("expected receiver failure")
	}
	if !strings.Contains(err.Error(), "receiver pipeline failed") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected receiver failure error, got %v", err)
	}
}

func TestRunFailsWhenSenderExits(t *testing.T) {
	fastRetries(t)

	readback := &fakeReadback{} // never succeeds
	var out bytes.Buffer

	err := New(&fakePreflight{}, blockingReceiver{}, failingSender{err: errors.New("kaboom")}, readback, x264Prober(), allocPort).
		Run(context.Background(), testConfig(), &out)
	if err == nil {
		t.Fatal("expected sender failure")
	}
	if !strings.Contains(err.Error(), "sender pipeline failed") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected sender failure error, got %v", err)
	}
}

func TestRunFailsWhenNoEncoder(t *testing.T) {
	var out bytes.Buffer

	err := New(&fakePreflight{}, blockingReceiver{}, blockingSender{}, &fakeReadback{}, fakeProber{}, allocPort).
		Run(context.Background(), testConfig(), &out)
	if err == nil || !strings.Contains(err.Error(), "no GStreamer H.264 encoder found") {
		t.Fatalf("expected no-encoder error, got %v", err)
	}
}

func TestRunTimesOutWhenReadbackNeverSucceeds(t *testing.T) {
	fastRetries(t)

	readback := &fakeReadback{} // never succeeds
	var out bytes.Buffer
	config := Config{Device: "/dev/video10", Frames: 5, Timeout: 60 * time.Millisecond}

	start := time.Now()
	err := New(&fakePreflight{}, blockingReceiver{}, blockingSender{}, readback, x264Prober(), allocPort).
		Run(context.Background(), config, &out)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !strings.Contains(err.Error(), "no frames yet") {
		t.Fatalf("expected timeout error to name last readback error, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
}

func TestRunReturnsCanceledWhenContextCancelled(t *testing.T) {
	fastRetries(t)

	readback := &fakeReadback{} // never succeeds
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- New(&fakePreflight{}, blockingReceiver{}, blockingSender{}, readback, x264Prober(), allocPort).
			Run(ctx, testConfig(), &out)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRunFailsOnPreflightError(t *testing.T) {
	preflight := &fakePreflight{err: errors.New("virtual camera /dev/video10 does not exist")}
	var out bytes.Buffer

	err := New(preflight, blockingReceiver{}, blockingSender{}, &fakeReadback{succeedAfter: 1}, x264Prober(), allocPort).
		Run(context.Background(), testConfig(), &out)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	if preflight.gotDevice != "/dev/video10" {
		t.Fatalf("expected preflight device /dev/video10, got %q", preflight.gotDevice)
	}
	if out.Len() != 0 {
		t.Fatalf("expected no output on preflight failure, got:\n%s", out.String())
	}
}

func TestConfigDefaults(t *testing.T) {
	c := Config{}.normalized()
	if c.Device != DefaultDevice || c.Frames != DefaultFrames || c.Timeout != DefaultTimeout {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}
