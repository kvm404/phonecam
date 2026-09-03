// Package smoke runs a deterministic, fully local RTP loopback self-test that
// proves H.264 frames can travel from a GStreamer sender, through the PhoneCam
// receiver, and out to the v4l2loopback virtual camera - without any phone.
package smoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/gstreamer"
	"github.com/kvm404/phonecam/linux-cli/internal/v4l2"
)

const (
	// DefaultDevice is the virtual camera the smoke test writes to and reads back.
	DefaultDevice = "/dev/video10"
	// DefaultFrames is the number of frames the readback must capture to pass.
	DefaultFrames = 30
	// DefaultTimeout bounds the whole self-test.
	DefaultTimeout = 15 * time.Second
	// loopbackHost is the address the receiver and sender rendezvous on.
	loopbackHost = "127.0.0.1"
)

// retryInterval is how often the readback pipeline is retried while the
// v4l2loopback device is warming up. It is a var so tests can shorten it.
var retryInterval = 500 * time.Millisecond

// encoderCandidates lists H.264 encoders to try, in order of preference.
var encoderCandidates = []string{"x264enc", "openh264enc", "avenc_h264", "vah264enc"}

// Preflight verifies the virtual camera device before the pipelines start.
type Preflight interface {
	Verify(device string) error
}

// Receiver runs the PhoneCam RTP receiver pipeline.
type Receiver interface {
	RunReceiver(ctx context.Context, port int, device string) error
}

// Sender runs the local videotestsrc -> RTP test sender pipeline.
type Sender interface {
	RunSender(ctx context.Context, port int, encoder string) error
}

// Readback runs the v4l2src -> fakesink readback pipeline.
type Readback interface {
	RunReadback(ctx context.Context, device string, frames int) error
}

// Prober reports whether a GStreamer element is installed.
type Prober interface {
	HasElement(element string) bool
}

// ListenPacket allocates a UDP socket, mirroring net.ListenPacket, so a free
// port can be chosen for the loopback stream.
type ListenPacket func(network, address string) (net.PacketConn, error)

// v4l2Preflight adapts v4l2.Verify to the Preflight interface using OSSystem.
type v4l2Preflight struct{}

func (v4l2Preflight) Verify(device string) error {
	return v4l2.Verify(v4l2.OSSystem{}, device)
}

// gstRunners adapts a gstreamer.Runner to the Receiver, Sender and Readback
// interfaces.
type gstRunners struct {
	runner gstreamer.Runner
}

func (g gstRunners) RunReceiver(ctx context.Context, port int, device string) error {
	return g.runner.Run(ctx, gstreamer.Config{RTPPort: port, Device: device})
}

func (g gstRunners) RunSender(ctx context.Context, port int, encoder string) error {
	return g.runner.RunSender(ctx, gstreamer.SenderConfig{
		Host:    loopbackHost,
		RTPPort: port,
		Encoder: encoder,
	})
}

func (g gstRunners) RunReadback(ctx context.Context, device string, frames int) error {
	return g.runner.RunReadback(ctx, gstreamer.ReadbackConfig{Device: device, Frames: frames})
}

// gstProber probes elements with gst-inspect-1.0.
type gstProber struct{}

func (gstProber) HasElement(element string) bool {
	return gstreamer.HasElement(nil, element)
}

// Config parameterizes a single smoke run.
type Config struct {
	Device  string
	RTPPort int
	Frames  int
	Timeout time.Duration
}

func (c Config) normalized() Config {
	if c.Device == "" {
		c.Device = DefaultDevice
	}
	if c.Frames == 0 {
		c.Frames = DefaultFrames
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

// Runtime wires the smoke test dependencies together.
type Runtime struct {
	preflight    Preflight
	receiver     Receiver
	sender       Sender
	readback     Readback
	prober       Prober
	listenPacket ListenPacket
}

// New builds a Runtime, defaulting any nil dependency to its real
// implementation, matching start.New's nil-defaulting pattern.
func New(preflight Preflight, receiver Receiver, sender Sender, readback Readback, prober Prober, listenPacket ListenPacket) Runtime {
	runners := gstRunners{runner: gstreamer.NewRunner(nil)}
	if preflight == nil {
		preflight = v4l2Preflight{}
	}
	if receiver == nil {
		receiver = runners
	}
	if sender == nil {
		sender = runners
	}
	if readback == nil {
		readback = runners
	}
	if prober == nil {
		prober = gstProber{}
	}
	if listenPacket == nil {
		listenPacket = net.ListenPacket
	}
	return Runtime{
		preflight:    preflight,
		receiver:     receiver,
		sender:       sender,
		readback:     readback,
		prober:       prober,
		listenPacket: listenPacket,
	}
}

// Run executes the loopback self-test, printing progress to stdout. It returns
// nil when real frames reach the device before the timeout, and a descriptive
// error otherwise.
func (r Runtime) Run(ctx context.Context, config Config, stdout io.Writer) error {
	config = config.normalized()

	if err := r.preflight.Verify(config.Device); err != nil {
		return err
	}

	encoder, err := r.pickEncoder()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Using H.264 encoder: %s\n", encoder)

	port := config.RTPPort
	if port == 0 {
		port, err = r.allocatePort()
		if err != nil {
			return err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	fmt.Fprintf(stdout, "Starting receiver on %s:%d -> %s\n", loopbackHost, port, config.Device)
	recvCtx, recvCancel := context.WithCancel(ctx)
	defer recvCancel()
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- r.receiver.RunReceiver(recvCtx, port, config.Device)
	}()

	fmt.Fprintf(stdout, "Starting test sender to %s:%d\n", loopbackHost, port)
	sendCtx, sendCancel := context.WithCancel(ctx)
	defer sendCancel()
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- r.sender.RunSender(sendCtx, port, encoder)
	}()

	fmt.Fprintf(stdout, "Reading back %d frames from %s\n", config.Frames, config.Device)
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	drainChild := func(ch <-chan error) {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
		}
	}

	var lastReadbackErr error
	for {
		lastReadbackErr = r.attemptReadback(ctx, config)
		if lastReadbackErr == nil {
			// Frames arrived. Tear down the sender and receiver; their
			// cancellation errors ("signal: killed" / context.Canceled) are
			// expected and swallowed.
			recvCancel()
			sendCancel()
			<-recvErr
			<-sendErr
			fmt.Fprintf(stdout, "Smoke test passed: %d frames reached %s\n", config.Frames, config.Device)
			return nil
		}

		select {
		case <-ctx.Done():
			recvCancel()
			sendCancel()
			drainChild(recvErr)
			drainChild(sendErr)
			return r.timeoutError(ctx, config.Timeout, lastReadbackErr)
		case e := <-recvErr:
			recvCancel()
			sendCancel()
			drainChild(sendErr)
			if ctx.Err() != nil {
				return r.timeoutError(ctx, config.Timeout, lastReadbackErr)
			}
			return exitError("receiver", e)
		case e := <-sendErr:
			recvCancel()
			sendCancel()
			drainChild(recvErr)
			if ctx.Err() != nil {
				return r.timeoutError(ctx, config.Timeout, lastReadbackErr)
			}
			return exitError("sender", e)
		case <-ticker.C:
		}
	}
}

// attemptReadback runs one readback pipeline attempt on a child context so it is
// bounded by the overall timeout and cleaned up promptly.
func (r Runtime) attemptReadback(ctx context.Context, config Config) error {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	return r.readback.RunReadback(readCtx, config.Device, config.Frames)
}

func (r Runtime) pickEncoder() (string, error) {
	for _, encoder := range encoderCandidates {
		if r.prober.HasElement(encoder) {
			return encoder, nil
		}
	}
	return "", errors.New("no GStreamer H.264 encoder found; install gst-plugins-ugly (x264enc)")
}

func (r Runtime) allocatePort() (int, error) {
	listener, err := r.listenPacket("udp", loopbackHost+":0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()

	addr, ok := listener.LocalAddr().(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("udp listener returned address %T", listener.LocalAddr())
	}
	return addr.Port, nil
}

// timeoutError reports why the test stopped without frames: a plain
// context.Canceled when the run was interrupted (so Ctrl+C exits cleanly), and
// a timeout message otherwise.
func (r Runtime) timeoutError(ctx context.Context, timeout time.Duration, lastErr error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if lastErr == nil {
		return fmt.Errorf("smoke test timed out after %s waiting for frames", timeout)
	}
	return fmt.Errorf("smoke test timed out after %s; last readback error: %w", timeout, lastErr)
}

// exitError describes a pipeline that exited on its own before the readback
// succeeded, which is always a failure.
func exitError(pipeline string, err error) error {
	if err == nil {
		return fmt.Errorf("%s pipeline exited unexpectedly before frames arrived", pipeline)
	}
	return fmt.Errorf("%s pipeline failed: %w", pipeline, err)
}
