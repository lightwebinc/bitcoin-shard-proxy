package worker

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/jefflightweb/bitcoin-shard-proxy/forwarder"
	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/sequence"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

// ── New ───────────────────────────────────────────────────────────────────────

func makeTestForwarder() *forwarder.Forwarder {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	counters := sequence.NewCounters(engine.NumGroups())
	return forwarder.New(engine, counters, 9001, true, nil, nil, false, nil)
}

func TestNew(t *testing.T) {
	fwd := makeTestForwarder()
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	w := New(0, fwd, ifaces, nil)
	if w == nil {
		t.Fatal("New returned nil")
	}
	if w.id != 0 {
		t.Errorf("id = %d, want 0", w.id)
	}
	if w.fwd == nil {
		t.Error("fwd should not be nil")
	}
	if w.log == nil {
		t.Error("log should not be nil")
	}
}

func TestNewNilRec(t *testing.T) {
	fwd := makeTestForwarder()
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	w := New(1, fwd, ifaces, nil)
	if w.rec != nil {
		t.Error("rec should be nil when not provided")
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

// ── NewTCPIngress ─────────────────────────────────────────────────────────────

func TestNewTCPIngress(t *testing.T) {
	fwd := makeTestForwarder()
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	ti := NewTCPIngress(fwd, ifaces, nil)
	if ti == nil {
		t.Fatal("NewTCPIngress returned nil")
	}
	if ti.fwd == nil {
		t.Error("fwd should not be nil")
	}
	if ti.log == nil {
		t.Error("log should not be nil")
	}
}

// ── handleConn ────────────────────────────────────────────────────────────────

// dialHandleConn opens a TCP loopback connection, runs handleConn in a
// goroutine, calls write() to populate the server side, then waits for
// handleConn to return (with a short timeout to prevent hangs).
func dialHandleConn(t *testing.T, write func(net.Conn)) {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("TCP loopback unavailable: %v", err)
	}
	defer ln.Close()

	clientConn, err := net.Dial("tcp6", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	serverConn, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	fwd := makeTestForwarder()
	ifaces := []*net.Interface{{Index: 1, Name: "lo"}}
	ti := NewTCPIngress(fwd, ifaces, nil)

	done := make(chan struct{})
	go func() {
		ti.handleConn(serverConn, nil)
		close(done)
	}()

	write(clientConn)
	clientConn.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("handleConn did not return within timeout")
	}
}

func buildTCPFrame(t *testing.T, txidByte byte, seq uint64, payload []byte) []byte {
	t.Helper()
	f := &frame.Frame{ShardSeqNum: seq, Payload: payload}
	f.TxID[0] = txidByte
	buf := make([]byte, frame.HeaderSize+len(payload))
	n, err := frame.Encode(f, buf)
	if err != nil {
		t.Fatalf("frame.Encode: %v", err)
	}
	return buf[:n]
}

func TestHandleConnValidFrame(t *testing.T) {
	raw := buildTCPFrame(t, 0xAB, 1, []byte("hello"))
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = conn.Write(raw)
	})
}

func TestHandleConnEmptyConn(t *testing.T) {
	dialHandleConn(t, func(_ net.Conn) {
		// write nothing — immediate EOF in handleConn
	})
}

func TestHandleConnTruncatedHeader(t *testing.T) {
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = conn.Write([]byte{0xE3, 0xE1, 0xF3}) // only 3 bytes
	})
}

func TestHandleConnBadMagic(t *testing.T) {
	hdr := make([]byte, frame.HeaderSize)
	hdr[0] = 0x00 // bad magic — all zeros
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = conn.Write(hdr)
	})
}

func TestHandleConnV1Frame(t *testing.T) {
	hdr := make([]byte, frame.HeaderSize)
	hdr[0], hdr[1], hdr[2], hdr[3] = 0xE3, 0xE1, 0xF3, 0xE8
	hdr[4], hdr[5] = 0x02, 0xBF
	hdr[6] = 0x01 // v1 — should be rejected
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = conn.Write(hdr)
	})
}

func TestHandleConnMultipleFrames(t *testing.T) {
	raw1 := buildTCPFrame(t, 0xAB, 1, nil)
	raw2 := buildTCPFrame(t, 0xCD, 2, []byte("world"))
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = io.MultiWriter(conn).Write(append(raw1, raw2...))
	})
}

func TestHandleConnPayloadTooLarge(t *testing.T) {
	hdr := make([]byte, frame.HeaderSize)
	hdr[0], hdr[1], hdr[2], hdr[3] = 0xE3, 0xE1, 0xF3, 0xE8
	hdr[4], hdr[5] = 0x02, 0xBF
	hdr[6] = frame.FrameVerV2
	// PayLen at bytes 80-83: set to MaxPayload + 1
	oversize := uint32(frame.MaxPayload + 1)
	hdr[80] = byte(oversize >> 24)
	hdr[81] = byte(oversize >> 16)
	hdr[82] = byte(oversize >> 8)
	hdr[83] = byte(oversize)
	dialHandleConn(t, func(conn net.Conn) {
		_, _ = conn.Write(hdr)
	})
}
