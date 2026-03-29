package worker

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	iface := &net.Interface{Index: 1, Name: "lo"}
	w := New(0, engine, iface, 9001, false, nil)
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
	iface := &net.Interface{Index: 1, Name: "lo"}
	w := New(1, engine, iface, 9001, true, nil)
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
	t.Cleanup(func() { conn.Close() })
	return conn, conn.LocalAddr().(*net.UDPAddr)
}

func makeWorker(t *testing.T, debug bool) *Worker {
	t.Helper()
	engine := shard.New(0xFF05, [11]byte{}, 8)
	iface := &net.Interface{Index: 1, Name: "lo"}
	return New(0, engine, iface, 9001, debug, nil)
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
	w.process(egress, raw, fakeAddr{})
}

func TestProcessValidFrameDebug(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, true)
	raw := validFrame(t)
	w.process(egress, raw, fakeAddr{})
}

func TestProcessInvalidFrame(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	// A buffer shorter than HeaderSize triggers ErrTooShort in frame.Decode.
	w.process(egress, []byte{0x00, 0x01}, fakeAddr{})
}

func TestProcessBadMagic(t *testing.T) {
	egress, _ := openLoopbackUDP(t)
	w := makeWorker(t, false)
	raw := make([]byte, 44) // all zeros — bad magic
	w.process(egress, raw, fakeAddr{})
}
