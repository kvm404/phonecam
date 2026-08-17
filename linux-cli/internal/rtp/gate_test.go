package rtp

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kvm404/phonecam/linux-cli/internal/pairing"
)

func TestNewGateEphemeralAndFixedPort(t *testing.T) {
	ephemeral, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate(0) failed: %v", err)
	}
	defer ephemeral.Close()
	if ephemeral.PublicRTPPort() <= 0 {
		t.Fatalf("expected assigned public port, got %d", ephemeral.PublicRTPPort())
	}
	if ephemeral.LocalRTPPort() <= 0 {
		t.Fatalf("expected assigned local port, got %d", ephemeral.LocalRTPPort())
	}
	if ephemeral.PublicRTPPort() == ephemeral.LocalRTPPort() {
		t.Fatal("public and local RTP ports must differ")
	}

	fixed, err := NewGate(ephemeral.PublicRTPPort())
	if err == nil {
		fixed.Close()
		t.Fatal("expected NewGate on an in-use public port to fail")
	}

	free, err := NewGate(0)
	if err != nil {
		t.Fatalf("second NewGate(0) failed: %v", err)
	}
	defer free.Close()
	port := free.PublicRTPPort()
	free.Close()

	rebound, err := NewGate(port)
	if err != nil {
		t.Fatalf("NewGate(%d) failed: %v", port, err)
	}
	defer rebound.Close()
	if rebound.PublicRTPPort() != port {
		t.Fatalf("expected public port %d, got %d", port, rebound.PublicRTPPort())
	}
}

func TestNewGateSetsPublicRcvbuf(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	if err := setRcvbuf(probe, PublicRcvbuf); err != nil {
		probe.Close()
		t.Fatalf("probe setRcvbuf: %v", err)
	}
	probeGot, err := getRcvbuf(probe)
	probe.Close()
	if err != nil {
		t.Fatalf("probe getRcvbuf: %v", err)
	}
	if min(probeGot, probeGot/2) < PublicRcvbuf {
		t.Skipf("kernel SO_RCVBUF effective %d < %d", min(probeGot, probeGot/2), PublicRcvbuf)
	}

	gate, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate failed: %v", err)
	}
	defer gate.Close()

	got, err := getRcvbuf(gate.public)
	if err != nil {
		t.Fatalf("getRcvbuf on public fd: %v", err)
	}
	if min(got, got/2) < PublicRcvbuf {
		t.Fatalf("public fd SO_RCVBUF effective %d, want >= %d (raw %d)", min(got, got/2), PublicRcvbuf, got)
	}
}

func TestGateDropsBeforeApprovalAndShortPackets(t *testing.T) {
	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	src := listenUDP(t, "127.0.0.1:0")
	defer src.Close()

	public := gatePublic(t, gate)
	if _, err := src.WriteTo(rtpPacket(99), public); err != nil {
		t.Fatalf("write pre-approval packet: %v", err)
	}
	if _, err := src.WriteTo([]byte{0x80, 0x00}, public); err != nil {
		t.Fatalf("write short packet: %v", err)
	}

	st := waitStats(t, gate, func(s Stats) bool {
		return s.Received >= 2 && s.DroppedACL >= 1 && s.DroppedShort >= 1
	})
	if st.Forwarded != 0 {
		t.Fatalf("expected no forwards before approval, got %+v", st)
	}
	assertNoInner(t, inner)
}

func TestGateForwardsExactPin(t *testing.T) {
	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	src := listenUDP(t, "127.0.0.1:0")
	defer src.Close()
	srcAddr := src.LocalAddr().(*net.UDPAddr)
	ssrc := uint32(0x11223344)
	gate.SetAllow(pairing.RTPSource{IP: srcAddr.IP, Port: srcAddr.Port, SSRC: ssrc})

	pkt := rtpPacket(ssrc)
	if _, err := src.WriteTo(pkt, gatePublic(t, gate)); err != nil {
		t.Fatalf("write pinned packet: %v", err)
	}

	got := readInner(t, inner)
	if !bytes.Equal(got, pkt) {
		t.Fatalf("forwarded packet mismatch: got %x want %x", got, pkt)
	}
	st := waitStats(t, gate, func(s Stats) bool { return s.Forwarded == 1 })
	if st.DroppedACL != 0 || st.LearnedIP || st.LastPacket.IsZero() {
		t.Fatalf("unexpected stats after exact pin: %+v", st)
	}
}

func TestGateACLDropsMismatchedPortAndSSRC(t *testing.T) {
	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	src := listenUDP(t, "127.0.0.1:0")
	defer src.Close()
	srcAddr := src.LocalAddr().(*net.UDPAddr)
	gate.SetAllow(pairing.RTPSource{IP: srcAddr.IP, Port: srcAddr.Port, SSRC: 7})

	other := listenUDP(t, "127.0.0.1:0")
	defer other.Close()
	if _, err := other.WriteTo(rtpPacket(7), gatePublic(t, gate)); err != nil {
		t.Fatalf("write wrong-port packet: %v", err)
	}
	if _, err := src.WriteTo(rtpPacket(8), gatePublic(t, gate)); err != nil {
		t.Fatalf("write wrong-ssrc packet: %v", err)
	}

	st := waitStats(t, gate, func(s Stats) bool { return s.DroppedACL >= 2 })
	if st.Forwarded != 0 {
		t.Fatalf("expected ACL drops only, got %+v", st)
	}
	assertNoInner(t, inner)
}

func TestGateLearnsDifferentIPAfterGraceWithZeroHTTPForwards(t *testing.T) {
	old := learnGrace
	learnGrace = time.Millisecond
	t.Cleanup(func() { learnGrace = old })

	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	httpSrc := listenUDP(t, "127.0.0.1:0")
	defer httpSrc.Close()
	httpAddr := httpSrc.LocalAddr().(*net.UDPAddr)

	ssrc := uint32(99)
	// Bind the phone to the announced source port so only the IP differs.
	phoneOnPort := listenUDP(t, net.JoinHostPort("127.0.0.2", strconv.Itoa(httpAddr.Port)))
	defer phoneOnPort.Close()

	gate.SetAllow(pairing.RTPSource{IP: httpAddr.IP, Port: httpAddr.Port, SSRC: ssrc})
	time.Sleep(5 * time.Millisecond)

	pkt := rtpPacket(ssrc)
	if _, err := phoneOnPort.WriteTo(pkt, gatePublic(t, gate)); err != nil {
		t.Fatalf("write learnable packet: %v", err)
	}

	got := readInner(t, inner)
	if !bytes.Equal(got, pkt) {
		t.Fatalf("expected learned packet to forward, got %x", got)
	}
	st := waitStats(t, gate, func(s Stats) bool { return s.LearnedIP && s.Forwarded == 1 })
	if st.DroppedACL != 0 {
		t.Fatalf("unexpected ACL drops during learn: %+v", st)
	}

	// A third IP must not learn again.
	other := listenUDP(t, net.JoinHostPort("127.0.0.3", strconv.Itoa(httpAddr.Port)))
	defer other.Close()
	if _, err := other.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write third-ip packet: %v", err)
	}
	st = waitStats(t, gate, func(s Stats) bool { return s.DroppedACL >= 1 })
	if st.Forwarded != 1 {
		t.Fatalf("expected third IP to be ACL-dropped, got %+v", st)
	}
}

func TestGateDoesNotLearnBeforeGraceOrAfterHTTPForward(t *testing.T) {
	gate, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate failed: %v", err)
	}
	defer gate.Close()

	var mu sync.Mutex
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	gate.nowFn = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = gate.Run(ctx) }()

	inner, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gate.LocalRTPPort()})
	if err != nil {
		t.Fatalf("bind inner: %v", err)
	}
	defer inner.Close()

	httpSrc := listenUDP(t, "127.0.0.1:0")
	defer httpSrc.Close()
	httpAddr := httpSrc.LocalAddr().(*net.UDPAddr)
	ssrc := uint32(5)
	gate.SetAllow(pairing.RTPSource{IP: httpAddr.IP, Port: httpAddr.Port, SSRC: ssrc})

	phone := listenUDP(t, net.JoinHostPort("127.0.0.2", strconv.Itoa(httpAddr.Port)))
	defer phone.Close()
	if _, err := phone.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write early roam packet: %v", err)
	}
	st := waitStats(t, gate, func(s Stats) bool { return s.DroppedACL >= 1 })
	if st.Forwarded != 0 || st.LearnedIP {
		t.Fatalf("learn must wait for grace: %+v", st)
	}
	assertNoInner(t, inner)

	if _, err := httpSrc.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write HTTP-IP packet: %v", err)
	}
	_ = readInner(t, inner)
	st = waitStats(t, gate, func(s Stats) bool { return s.Forwarded == 1 })
	if st.LearnedIP {
		t.Fatalf("HTTP-IP forward must not learn: %+v", st)
	}

	// Grace has elapsed, but HTTP-IP already forwarded — pin stays frozen.
	mu.Lock()
	now = now.Add(learnGrace + time.Second)
	mu.Unlock()

	if _, err := phone.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write roam after HTTP forward: %v", err)
	}
	st = waitStats(t, gate, func(s Stats) bool { return s.DroppedACL >= 2 })
	if st.LearnedIP || st.Forwarded != 1 {
		t.Fatalf("HTTP-IP forward must freeze the pin after grace: %+v", st)
	}
}

func TestSetAllowResetsLearnState(t *testing.T) {
	old := learnGrace
	learnGrace = time.Millisecond
	t.Cleanup(func() { learnGrace = old })

	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	httpSrc := listenUDP(t, "127.0.0.1:0")
	defer httpSrc.Close()
	httpAddr := httpSrc.LocalAddr().(*net.UDPAddr)
	ssrc := uint32(3)
	allow := pairing.RTPSource{IP: httpAddr.IP, Port: httpAddr.Port, SSRC: ssrc}
	gate.SetAllow(allow)

	if _, err := httpSrc.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write HTTP-IP packet: %v", err)
	}
	_ = readInner(t, inner)

	gate.SetAllow(allow)
	if gate.Stats().LearnedIP {
		t.Fatal("SetAllow must clear learnedIP")
	}
	time.Sleep(5 * time.Millisecond)

	phone := listenUDP(t, net.JoinHostPort("127.0.0.2", strconv.Itoa(httpAddr.Port)))
	defer phone.Close()
	if _, err := phone.WriteTo(rtpPacket(ssrc), gatePublic(t, gate)); err != nil {
		t.Fatalf("write roam after reset: %v", err)
	}
	_ = readInner(t, inner)
	st := waitStats(t, gate, func(s Stats) bool { return s.LearnedIP })
	if st.Forwarded < 2 {
		t.Fatalf("expected learn after SetAllow reset, got %+v", st)
	}
}

func TestSetAllowZeroIPDropsAll(t *testing.T) {
	gate, inner := startGate(t)
	defer gate.Close()
	defer inner.Close()

	src := listenUDP(t, "127.0.0.1:0")
	defer src.Close()
	srcAddr := src.LocalAddr().(*net.UDPAddr)
	gate.SetAllow(pairing.RTPSource{IP: srcAddr.IP, Port: srcAddr.Port, SSRC: 1})
	gate.SetAllow(pairing.RTPSource{})

	if _, err := src.WriteTo(rtpPacket(1), gatePublic(t, gate)); err != nil {
		t.Fatalf("write after zero allow: %v", err)
	}
	st := waitStats(t, gate, func(s Stats) bool { return s.DroppedACL >= 1 })
	if st.Forwarded != 0 {
		t.Fatalf("zero IP must drop all, got %+v", st)
	}
	assertNoInner(t, inner)
}

func TestInnerPortRejectsLANSourcedDatagram(t *testing.T) {
	gate, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate failed: %v", err)
	}
	defer gate.Close()

	inner, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gate.LocalRTPPort()})
	if err != nil {
		t.Fatalf("bind inner loopback port: %v", err)
	}
	defer inner.Close()
	if !inner.LocalAddr().(*net.UDPAddr).IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("inner socket must bind 127.0.0.1, got %v", inner.LocalAddr())
	}

	lanIP := firstNonLoopbackIPv4()
	if lanIP == nil {
		t.Log("no non-loopback IPv4; asserting loopback bind only")
		return
	}

	src, err := net.ListenUDP("udp", &net.UDPAddr{IP: lanIP, Port: 0})
	if err != nil {
		t.Fatalf("bind LAN source: %v", err)
	}
	defer src.Close()

	// A LAN-sourced datagram aimed at the inner port on the LAN address is not
	// the success path: the socket is bound only to 127.0.0.1.
	if _, err := src.WriteTo(rtpPacket(1), &net.UDPAddr{IP: lanIP, Port: gate.LocalRTPPort()}); err != nil {
		t.Fatalf("write LAN datagram: %v", err)
	}
	assertNoInner(t, inner)
}

func TestRefreshLocalPortAvoidsPrevious(t *testing.T) {
	gate, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate failed: %v", err)
	}
	defer gate.Close()

	first := gate.LocalRTPPort()
	if err := gate.RefreshLocalPort(); err != nil {
		t.Fatalf("RefreshLocalPort failed: %v", err)
	}
	if gate.LocalRTPPort() == first {
		t.Fatalf("expected a new local port, still %d", first)
	}
}

func startGate(t *testing.T) (*Gate, *net.UDPConn) {
	t.Helper()

	gate, err := NewGate(0)
	if err != nil {
		t.Fatalf("NewGate failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = gate.Run(ctx) }()

	inner, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gate.LocalRTPPort()})
	if err != nil {
		gate.Close()
		t.Fatalf("bind inner: %v", err)
	}
	return gate, inner
}

func listenUDP(t *testing.T, address string) *net.UDPConn {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		t.Fatalf("resolve %s: %v", address, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", address, err)
	}
	return conn
}

func gatePublic(t *testing.T, gate *Gate) *net.UDPAddr {
	t.Helper()
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: gate.PublicRTPPort()}
}

func rtpPacket(ssrc uint32) []byte {
	pkt := make([]byte, rtpHeaderSize)
	pkt[0] = 0x80
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	return pkt
}

func readInner(t *testing.T, inner *net.UDPConn) []byte {
	t.Helper()
	_ = inner.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := inner.ReadFrom(buf)
	if err != nil {
		t.Fatalf("expected forwarded datagram on inner port: %v", err)
	}
	return append([]byte(nil), buf[:n]...)
}

func assertNoInner(t *testing.T, inner *net.UDPConn) {
	t.Helper()
	_ = inner.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	buf := make([]byte, 64)
	n, addr, err := inner.ReadFrom(buf)
	if err == nil {
		t.Fatalf("inner port received %d bytes from %v; LAN/unpinned path must not succeed", n, addr)
	}
}

func waitStats(t *testing.T, gate *Gate, pred func(Stats) bool) Stats {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Stats
	for time.Now().Before(deadline) {
		last = gate.Stats()
		if pred(last) {
			return last
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stats never matched: %+v", last)
	return last
}

func firstNonLoopbackIPv4() net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4
			}
		}
	}
	return nil
}
