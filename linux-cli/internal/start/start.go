package start

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/control"
	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
)

const DefaultVirtualCamera = "/dev/video10"

type System interface {
	Hostname() (string, error)
	Interfaces() ([]net.Interface, error)
	InterfaceAddrs(net.Interface) ([]net.Addr, error)
	Listen(network, address string) (net.Listener, error)
	ListenPacket(network, address string) (net.PacketConn, error)
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
	system System
}

func New(system System) Runtime {
	if system == nil {
		system = OSSystem{}
	}
	return Runtime{system: system}
}

func (r Runtime) Run(ctx context.Context, config Config, stdout io.Writer) error {
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

	writeStartOutput(stdout, config.VirtualCamera, session)

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
	}
}

func writeStartOutput(w io.Writer, virtualCamera string, session *pairing.Session) {
	if virtualCamera == "" {
		virtualCamera = DefaultVirtualCamera
	}

	payload := session.Payload()
	payloadJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		payloadJSON = []byte("{}")
	}

	fmt.Fprintln(w, "PhoneCam")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Virtual camera: %s\n", virtualCamera)
	fmt.Fprintln(w, "Status: Waiting for phone")
	fmt.Fprintf(w, "Control server: %s\n", payload.Control)
	fmt.Fprintf(w, "RTP endpoint: %s\n", payload.RTP)
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
