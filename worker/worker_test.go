package worker

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/jefflightweb/bitcoin-shard-proxy/forwarder"
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
