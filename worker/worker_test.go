package worker

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"
	"testing"

	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	w := New(0, engine, ifaces, 9001, false, nil)
	if w == nil {
		t.Fatal("New returned nil")
	}
	if w.id != 0 {
		t.Errorf("id = %d, want 0", w.id)
	}
	if w.port != 9001 {
		t.Errorf("port = %d, want 9001", w.port)
	}
	if w.debug {
		t.Error("debug should be false")
	}
	if w.log == nil {
		t.Error("log should not be nil")
	}
}

func TestNewDebugMode(t *testing.T) {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	w := New(1, engine, ifaces, 9001, true, nil)
	if !w.debug {
		t.Error("debug should be true")
	}
}

// ── isClosedErr ───────────────────────────────────────────────────────────────

func TestIsClosedErrNil(t *testing.T) {
	if isClosedErr(nil) {
		t.Error("nil should not be a closed error")
	}
}

func TestIsClosedErrNetErrClosed(t *testing.T) {
	if !isClosedErr(net.ErrClosed) {
		t.Error("net.ErrClosed should be recognised as a closed error")
	}
}

func TestIsClosedErrEBADF(t *testing.T) {
	if !isClosedErr(syscall.EBADF) {
		t.Error("EBADF should be recognised as a closed error")
	}
}

func TestIsClosedErrEINVAL(t *testing.T) {
	if !isClosedErr(syscall.EINVAL) {
		t.Error("EINVAL should be recognised as a closed error")
	}
}

func TestIsClosedErrWrapped(t *testing.T) {
	wrapped := &net.OpError{Op: "read", Err: net.ErrClosed}
	if !isClosedErr(wrapped) {
		t.Error("wrapped net.ErrClosed should be recognised as a closed error")
	}
}

func TestIsClosedErrUnrelated(t *testing.T) {
	if isClosedErr(syscall.ECONNREFUSED) {
		t.Error("ECONNREFUSED should not be a closed error")
	}
}

func TestIsClosedErrGeneric(t *testing.T) {
	if isClosedErr(errors.New("some other error")) {
		t.Error("generic error should not be a closed error")
	}
}

// ── isErrno ───────────────────────────────────────────────────────────────────

func TestIsErrnoDirectMatch(t *testing.T) {
	if !isErrno(syscall.EBADF, syscall.EBADF) {
		t.Error("direct EBADF should match")
	}
}

func TestIsErrnoNoMatch(t *testing.T) {
	if isErrno(syscall.EAGAIN, syscall.EBADF) {
		t.Error("EAGAIN should not match EBADF")
	}
}

func TestIsErrnoNil(t *testing.T) {
	if isErrno(nil, syscall.EBADF) {
		t.Error("nil error should not match")
	}
}

func TestIsErrnoWrapped(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", syscall.EBADF)
	if !isErrno(wrapped, syscall.EBADF) {
		t.Error("wrapped EBADF should match")
	}
}

// ── unwrap ────────────────────────────────────────────────────────────────────

func TestUnwrapNil(t *testing.T) {
	if unwrap(nil) != nil {
		t.Error("unwrap(nil) should return nil")
	}
}

func TestUnwrapNoChain(t *testing.T) {
	if unwrap(errors.New("plain")) != nil {
		t.Error("unwrap of non-wrapping error should return nil")
	}
}

func TestUnwrapChain(t *testing.T) {
	inner := errors.New("inner")
	outer := fmt.Errorf("outer: %w", inner)
	if unwrap(outer) != inner {
		t.Error("unwrap should return the wrapped inner error")
	}
}

// ── process ───────────────────────────────────────────────────────────────────

// fakeAddr implements net.Addr for use as a dummy source address.
type fakeAddr struct{}

func (fakeAddr) Network() string { return "udp6" }
func (fakeAddr) String() string  { return "[::1]:12345" }

// openLoopbackUDP opens a connected UDP socket to loopback and returns the
// conn along with its bound address. Skips the test if no loopback is usable.
func openLoopbackUDP(t *testing.T) (*net.UDPConn, *net.UDPAddr) {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp6", "[::1]:0")
	if err != nil {
		t.Skipf("udp6 loopback not available: %v", err)
	}
	conn, err := net.ListenUDP("udp6", addr)
	if err != nil {
		t.Skipf("ListenUDP loopback: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, conn.LocalAddr().(*net.UDPAddr)
}

func makeWorker(t *testing.T, debug bool) *Worker {
	t.Helper()
	engine := shard.New(0xFF05, [11]byte{}, 8)
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	return New(0, engine, ifaces, 9001, debug, nil)
}

func makeTargets(t *testing.T, conns ...*net.UDPConn) []egressTarget {
	t.Helper()
	tgts := make([]egressTarget, len(conns))
	for i, c := range conns {
		tgts[i] = egressTarget{iface: &net.Interface{Index: i + 1, Name: fmt.Sprintf("lo%d", i)}, conn: c}
	}
	return tgts
}

func validFrame(t *testing.T) []byte {
	t.Helper()
	// Manually build a minimal valid BSV frame header + empty payload.
	// Magic: 0xE3E1F3E8, ProtoVer: 0x02BF, FrameVer: 0x01, Reserved: 0x00
	// TxID: 32 zero bytes, PayloadLen: 0x00000000
	buf := make([]byte, 44)
	buf[0], buf[1], buf[2], buf[3] = 0xE3, 0xE1, 0xF3, 0xE8
	buf[4], buf[5] = 0x02, 0xBF
	buf[6] = 0x01
	return buf
}

func TestProcessValidFrame(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	raw := validFrame(t)
	// process writes to egress; with a zero TxID the group index is 0 and the
	// destination is a multicast addr that won't be reachable on loopback, but
	// WriteTo is expected to fail gracefully — no panic.
	w.process(makeTargets(t, egress), raw, fakeAddr{})
}

func TestProcessValidFrameDebug(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, true)
	raw := validFrame(t)
	w.process(makeTargets(t, egress), raw, fakeAddr{})
}

func TestProcessInvalidFrame(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	// A buffer shorter than HeaderSize triggers ErrTooShort in frame.Decode.
	w.process(makeTargets(t, egress), []byte{0x00, 0x01}, fakeAddr{})
}

func TestProcessBadMagic(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	raw := make([]byte, 44) // all zeros — bad magic
	w.process(makeTargets(t, egress), raw, fakeAddr{})
}

// ── probeEgressSocket ────────────────────────────────────────────────────────

func TestProbeEgressSocketLoopback(t *testing.T) {
	conn, _ := openLoopbackUDP(t)
	iface := &net.Interface{Index: 1, Name: "lo"}
	// On loopback with a real UDP socket the probe should either succeed or
	// return a soft error (non-fatal). It must never return a hard error that
	// would prevent startup on a properly configured host.
	if err := probeEgressSocket(slog.Default(), conn, iface); err != nil {
		t.Errorf("probeEgressSocket on loopback: unexpected hard error: %v", err)
	}
}

func TestProbeEgressSocketClosedConn(t *testing.T) {
	conn, _ := openLoopbackUDP(t)
	iface := &net.Interface{Index: 1, Name: "lo"}
	// Closing the conn before probing should not produce a hard-error return
	// (EBADF is treated as soft).
	_ = conn.Close()
	// Should not panic; soft or hard is acceptable but must not crash.
	_ = probeEgressSocket(slog.Default(), conn, iface)
}

func TestProcessMultipleTargets(t *testing.T) {
	egress1, _ := openLoopbackUDP(t)
	egress2, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	raw := validFrame(t)
	// Fan-out to two targets; both writes may fail gracefully on loopback
	// multicast — no panic, no hang.
	w.process(makeTargets(t, egress1, egress2), raw, fakeAddr{})
}
