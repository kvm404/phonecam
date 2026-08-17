package rtp

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
)

// PublicRcvbuf is the SO_RCVBUF set on the LAN-facing UDP socket. It matches
// the inner udpsrc buffer-size so an IDR burst is not dropped in the kernel.
const PublicRcvbuf = 4194304

const rtpHeaderSize = 12

// learnGrace is how long after SetAllow a different-IP port+SSRC match must
// wait, and only if the HTTP-peer pin has forwarded nothing. Tests shrink this.
var learnGrace = time.Second

type Stats struct {
	Received     uint64
	Forwarded    uint64
	DroppedACL   uint64
	DroppedShort uint64
	LastPacket   time.Time
	LearnedIP    bool
}

type Gate struct {
	public     net.PacketConn
	publicPort int

	mu        sync.RWMutex
	localPort int
	allow     pairing.RTPSource
	allowSet  bool
	learnedIP bool
	httpFwd   bool
	eligible  time.Time

	received     atomic.Uint64
	forwarded    atomic.Uint64
	droppedACL   atomic.Uint64
	droppedShort atomic.Uint64
	lastPacket   atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}

	nowFn func() time.Time
}

func NewGate(publicPort int) (*Gate, error) {
	if publicPort < 0 || publicPort > 65535 {
		return nil, fmt.Errorf("invalid RTP port %d", publicPort)
	}

	address := "0.0.0.0:0"
	if publicPort > 0 {
		address = fmt.Sprintf("0.0.0.0:%d", publicPort)
	}
	public, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, err
	}
	udpAddr, ok := public.LocalAddr().(*net.UDPAddr)
	if !ok {
		_ = public.Close()
		return nil, fmt.Errorf("udp listener returned address %T", public.LocalAddr())
	}

	g := &Gate{
		public:     public,
		publicPort: udpAddr.Port,
		closed:     make(chan struct{}),
	}
	if err := setRcvbuf(public, PublicRcvbuf); err != nil {
		_ = public.Close()
		return nil, err
	}
	if err := g.RefreshLocalPort(); err != nil {
		_ = public.Close()
		return nil, err
	}
	return g, nil
}

func (g *Gate) LocalRTPPort() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.localPort
}

func (g *Gate) PublicRTPPort() int {
	return g.publicPort
}

// RefreshLocalPort probes a free 127.0.0.1 port, closes that socket, and
// stores the number for GStreamer. It will not reuse the port that just failed.
func (g *Gate) RefreshLocalPort() error {
	g.mu.RLock()
	avoid := g.localPort
	g.mu.RUnlock()

	const attempts = 8
	for i := 0; i < attempts; i++ {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		addr, ok := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if !ok {
			return fmt.Errorf("udp listener returned address %T", conn.LocalAddr())
		}
		if addr.Port == 0 || (avoid != 0 && addr.Port == avoid) {
			continue
		}
		g.mu.Lock()
		g.localPort = addr.Port
		g.mu.Unlock()
		return nil
	}
	return fmt.Errorf("could not reserve a new local RTP port")
}

func (g *Gate) SetAllow(src pairing.RTPSource) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.allow = pairing.RTPSource{
		IP:   append(net.IP(nil), src.IP...),
		Port: src.Port,
		SSRC: src.SSRC,
	}
	g.allowSet = src.IP != nil && !src.IP.IsUnspecified()
	g.learnedIP = false
	g.httpFwd = false
	g.eligible = g.now().Add(learnGrace)
}

func (g *Gate) Stats() Stats {
	var last time.Time
	if ns := g.lastPacket.Load(); ns != 0 {
		last = time.Unix(0, ns)
	}
	g.mu.RLock()
	learned := g.learnedIP
	g.mu.RUnlock()
	return Stats{
		Received:     g.received.Load(),
		Forwarded:    g.forwarded.Load(),
		DroppedACL:   g.droppedACL.Load(),
		DroppedShort: g.droppedShort.Load(),
		LastPacket:   last,
		LearnedIP:    learned,
	}
}

func (g *Gate) Close() error {
	var err error
	g.closeOnce.Do(func() {
		close(g.closed)
		if g.public != nil {
			err = g.public.Close()
		}
	})
	return err
}

func (g *Gate) Run(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			_ = g.Close()
		case <-g.closed:
		}
	}()

	buf := make([]byte, 65535)
	for {
		n, addr, err := g.public.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-g.closed:
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return nil
			default:
			}
			return err
		}
		g.handle(buf[:n], addr)
	}
}

func (g *Gate) handle(pkt []byte, addr net.Addr) {
	g.received.Add(1)
	if len(pkt) < rtpHeaderSize {
		g.droppedShort.Add(1)
		return
	}

	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		g.droppedACL.Add(1)
		return
	}

	ssrc := binary.BigEndian.Uint32(pkt[8:12])

	g.mu.RLock()
	allow := g.allow
	allowSet := g.allowSet
	learned := g.learnedIP
	httpFwd := g.httpFwd
	eligible := g.eligible
	localPort := g.localPort
	g.mu.RUnlock()

	if !allowSet {
		g.droppedACL.Add(1)
		return
	}

	exact := allow.IP.Equal(udpAddr.IP) && allow.Port == udpAddr.Port && allow.SSRC == ssrc
	if exact {
		g.markHTTPForwarded(udpAddr.IP, udpAddr.Port, ssrc)
		g.forward(pkt, localPort)
		return
	}

	portSSRC := allow.Port == udpAddr.Port && allow.SSRC == ssrc
	eligibleNow := !httpFwd && !g.now().Before(eligible)
	if learned || !eligibleNow || !portSSRC {
		g.droppedACL.Add(1)
		return
	}

	if !g.tryLearn(udpAddr.IP, udpAddr.Port, ssrc) {
		g.droppedACL.Add(1)
		return
	}
	g.forward(pkt, localPort)
}

func (g *Gate) markHTTPForwarded(ip net.IP, port int, ssrc uint32) {
	g.mu.Lock()
	if g.allowSet && g.allow.IP.Equal(ip) && g.allow.Port == port && g.allow.SSRC == ssrc {
		g.httpFwd = true
	}
	g.mu.Unlock()
}

func (g *Gate) tryLearn(ip net.IP, port int, ssrc uint32) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.allowSet || g.learnedIP || g.httpFwd {
		return false
	}
	if g.now().Before(g.eligible) {
		return false
	}
	if g.allow.Port != port || g.allow.SSRC != ssrc {
		return false
	}
	if g.allow.IP.Equal(ip) {
		return false
	}
	g.allow.IP = append(net.IP(nil), ip...)
	g.learnedIP = true
	return true
}

func (g *Gate) forward(pkt []byte, localPort int) {
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: localPort}
	if _, err := g.public.WriteTo(pkt, dst); err != nil {
		return
	}
	g.forwarded.Add(1)
	g.lastPacket.Store(g.now().UnixNano())
}

func (g *Gate) now() time.Time {
	if g.nowFn != nil {
		return g.nowFn()
	}
	return time.Now()
}

func setRcvbuf(conn net.PacketConn, size int) error {
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return fmt.Errorf("packet conn is %T, not *net.UDPConn", conn)
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return err
	}
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, size)
	}); err != nil {
		return err
	}
	return sockErr
}
