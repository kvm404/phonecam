package start

import (
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
	"github.com/kvm404/phonecam/linux-cli/internal/v4l2"
)

const DefaultVirtualCamera = "/dev/video10"

// approvalPollInterval is how often the run loop checks whether the pairing
// session has been approved before starting the receiver. It is a package-level
// var so tests can shorten it.
var approvalPollInterval = 200 * time.Millisecond

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
}

type Runtime struct {
	system    System
	receiver  Receiver
	preflight Preflight
}

func New(system System, receiver Receiver, preflight Preflight) Runtime {
	if system == nil {
		system = OSSystem{}
	}
	if receiver == nil {
		receiver = gstreamer.NewRunner(nil)
	}
	if preflight == nil {
		preflight = v4l2Preflight{}
	}
	return Runtime{system: system, receiver: receiver, preflight: preflight}
}

func (r Runtime) Run(ctx context.Context, config Config, stdout io.Writer) error {
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
		return err
	}
	defer controlListener.Close()

	rtpPort := config.RTPPort
	if rtpPort == 0 {
		rtpListener, err := listenUDP(r.system)
		if err != nil {
			return err
		}
		rtpPort = rtpListener.LocalAddr().(*net.UDPAddr).Port
		_ = rtpListener.Close()
	}

	hostname, err := r.system.Hostname()
	if err != nil || hostname == "" {
		hostname = "phonecam-linux"
	}

	controlPort := controlListener.Addr().(*net.TCPAddr).Port
	now := config.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	session, err := pairing.New(pairing.Config{
		LaptopName: hostname,
		ControlURL: fmt.Sprintf("http://%s:%d", host, controlPort),
		RTPHost:    host,
		RTPPort:    rtpPort,
		Now:        now,
	})
	if err != nil {
		return err
	}

	server := &http.Server{
		Handler: control.New(control.Config{Session: session}).Handler(),
	}
	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(controlListener); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	writeStartOutput(stdout, virtualCamera, session)

	// Do not start the receiver until the phone has been approved. The phone
	// reports its actual negotiated video profile during pairing, which we use
	// to size the receiver pipeline.
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()

	for !session.IsApproved() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
			session.Invalidate()
			return ctx.Err()
		case err := <-errCh:
			session.Invalidate()
			return err
		case <-ticker.C:
		}
	}

	video := session.NegotiatedVideo()
	fmt.Fprintf(stdout, "Phone connected: receiving %dx%d@%d video\n", video.Width, video.Height, video.FPS)

	receiverErr := make(chan error, 1)
	go func() {
		receiverErr <- r.receiver.Run(recvCtx, gstreamer.Config{
			RTPPort: rtpPort,
			Device:  virtualCamera,
			Width:   video.Width,
			Height:  video.Height,
			FPS:     video.FPS,
		})
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		session.Invalidate()
		return ctx.Err()
	case err := <-errCh:
		session.Invalidate()
		return err
	case err := <-receiverErr:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		session.Invalidate()
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

func writeStartOutput(w io.Writer, virtualCamera string, session *pairing.Session) {
	payload := session.Payload()
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

func listenUDP(system System) (net.PacketConn, error) {
	listener, err := system.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	if _, ok := listener.LocalAddr().(*net.UDPAddr); !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("udp listener returned address %T", listener.LocalAddr())
	}
	return listener, nil
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
