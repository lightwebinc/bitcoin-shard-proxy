package forwarder

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"syscall"
	"testing"

	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/sequence"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

// ── helpers ───────────────────────────────────────────────────────────────────

type fakeAddr struct{}

func (fakeAddr) Network() string { return "udp6" }
func (fakeAddr) String() string  { return "[::1]:12345" }

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

func makeTargets(t *testing.T, conns ...*net.UDPConn) []Target {
	t.Helper()
	tgts := make([]Target, len(conns))
	for i, c := range conns {
		tgts[i] = Target{
			Iface: &net.Interface{Index: i + 1, Name: "lo"},
			Conn:  c,
		}
	}
	return tgts
}

func buildV2Frame(t *testing.T, txidByte0 byte, shardSeqNum uint64, subtreeHeight uint8, payload []byte) []byte {
	t.Helper()
	f := &frame.Frame{
		ShardSeqNum:   shardSeqNum,
		SubtreeHeight: subtreeHeight,
		Payload:       payload,
	}
	f.TxID[0] = txidByte0
	buf := make([]byte, frame.HeaderSize+len(payload))
	n, err := frame.Encode(f, buf)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return buf[:n]
}

func buildV1Frame(t *testing.T, txidByte0 byte, payload []byte) []byte {
	t.Helper()
	buf := make([]byte, frame.HeaderSizeV1+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], frame.MagicBSV)
	binary.BigEndian.PutUint16(buf[4:6], frame.ProtoVer)
	buf[6] = frame.FrameVerV1
	buf[8] = txidByte0
	binary.BigEndian.PutUint32(buf[40:44], uint32(len(payload)))
	copy(buf[44:], payload)
	return buf
}

func makeForwarder(proxySeqEnabled bool, staticID []byte, staticHeight *uint8) *Forwarder {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	counters := sequence.NewCounters(engine.NumGroups())
	return New(engine, counters, 9001, proxySeqEnabled, staticID, staticHeight, false, nil)
}

func encodeBuf() []byte { return make([]byte, frame.HeaderSize+frame.MaxPayload) }

// ── zero-copy fast path ───────────────────────────────────────────────────────

func TestProcessSenderAssignedSeqPassthrough(t *testing.T) {
	conn, _ := openLoopbackUDP(t)
	fw := makeForwarder(true, nil, nil)
	raw := buildV2Frame(t, 0xAB, 999, 0, nil)
	// ShardSeqNum != 0 and no static overrides: fast path (zero-copy).
	// WriteTo to multicast dst will fail on loopback — that's fine, no panic.
	fw.Process(makeTargets(t, conn), encodeBuf(), raw, fakeAddr{}, 0)
}

// ── proxy-assigned sequence ───────────────────────────────────────────────────

func TestProcessProxyAssignsSeqWhenZero(t *testing.T) {
	fw := makeForwarder(true, nil, nil)
	raw := buildV2Frame(t, 0x00, 0, 0, nil)

	buf := encodeBuf()
	var encoded []byte
	// Capture written bytes by intercepting at encode step via a sink target.
	// We use a real loopback socket but the write will fail — we verify seq
	// by re-decoding the encodeBuf after the call.

	// Process once — should stamp seq=0.
	conn, _ := openLoopbackUDP(t)
	targets := makeTargets(t, conn)
	fw.Process(targets, buf, raw, fakeAddr{}, 0)

	// First call stamps seq=0.
	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.ShardSeqNum != 0 {
		t.Errorf("first call: ShardSeqNum = %d, want 0", got.ShardSeqNum)
	}

	// Second call should stamp seq=1.
	raw2 := buildV2Frame(t, 0x00, 0, 0, nil)
	buf2 := encodeBuf()
	fw.Process(targets, buf2, raw2, fakeAddr{}, 0)
	got2, err := frame.Decode(buf2[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode 2: %v", err)
	}
	if got2.ShardSeqNum != 1 {
		t.Errorf("second call: ShardSeqNum = %d, want 1", got2.ShardSeqNum)
	}
	_ = encoded
}

func TestProcessProxySeqDisabled(t *testing.T) {
	fw := makeForwarder(false, nil, nil)
	raw := buildV2Frame(t, 0x00, 0, 0, nil)
	buf := encodeBuf()
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)
	// ProxySeq disabled: no re-encode, buf unchanged from initial zeros.
	// Verify buf[40:48] (ShardSeqNum) is still 0 (encodeBuf not written).
	for _, b := range buf[40:48] {
		if b != 0 {
			t.Error("encodeBuf was written but ProxySeq disabled — expected fast path")
			break
		}
	}
}

// ── static overrides ──────────────────────────────────────────────────────────

func TestProcessStaticSubtreeIDOverride(t *testing.T) {
	staticID := make([]byte, 32)
	for i := range staticID {
		staticID[i] = 0xBB
	}
	fw := makeForwarder(false, staticID, nil)
	raw := buildV2Frame(t, 0x00, 1, 0, nil)
	buf := encodeBuf()
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)

	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	for i, b := range got.SubtreeID {
		if b != 0xBB {
			t.Errorf("SubtreeID[%d] = 0x%02X, want 0xBB", i, b)
			break
		}
	}
}

func TestProcessStaticSubtreeHeightOverride(t *testing.T) {
	h := uint8(15)
	fw := makeForwarder(false, nil, &h)
	raw := buildV2Frame(t, 0x00, 1, 0, nil)
	buf := encodeBuf()
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)

	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.SubtreeHeight != 15 {
		t.Errorf("SubtreeHeight = %d, want 15", got.SubtreeHeight)
	}
}

func TestProcessStaticSubtreeHeightZero(t *testing.T) {
	h := uint8(0)
	fw := makeForwarder(false, nil, &h)
	raw := buildV2Frame(t, 0x00, 1, 20, nil)
	buf := encodeBuf()
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)

	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.SubtreeHeight != 0 {
		t.Errorf("SubtreeHeight = %d, want 0 (explicit zero override)", got.SubtreeHeight)
	}
}

// ── error paths ───────────────────────────────────────────────────────────────

func TestProcessInvalidFrame(t *testing.T) {
	fw := makeForwarder(true, nil, nil)
	conn, _ := openLoopbackUDP(t)
	// Truncated buffer — must not panic.
	fw.Process(makeTargets(t, conn), encodeBuf(), []byte{0x00, 0x01}, fakeAddr{}, 0)
}

func TestProcessBadMagic(t *testing.T) {
	fw := makeForwarder(true, nil, nil)
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), encodeBuf(), make([]byte, frame.HeaderSize), fakeAddr{}, 0)
}

func TestProcessV1FrameRejected(t *testing.T) {
	fw := makeForwarder(true, nil, nil)
	conn, _ := openLoopbackUDP(t)
	v1 := make([]byte, 44)
	v1[0], v1[1], v1[2], v1[3] = 0xE3, 0xE1, 0xF3, 0xE8
	v1[6] = 0x01 // v1 FrameVer
	fw.Process(makeTargets(t, conn), encodeBuf(), v1, fakeAddr{}, 0)
	// Must not panic; decode error is handled gracefully.
}

func TestProcessMultipleTargets(t *testing.T) {
	conn1, _ := openLoopbackUDP(t)
	conn2, _ := openLoopbackUDP(t)
	fw := makeForwarder(true, nil, nil)
	raw := buildV2Frame(t, 0xAB, 1, 0, nil)
	fw.Process(makeTargets(t, conn1, conn2), encodeBuf(), raw, fakeAddr{}, 0)
}

// ── v1 ingress \u2192 v2 egress ──────────────────────────────────────────────────────

func TestProcessV1FrameReencode(t *testing.T) {
	// v1 frames have a 44-byte header; they must always be re-encoded to v2.
	// Use empty payload so the re-encoded v2 frame fits in exactly HeaderSize bytes.
	raw := buildV1Frame(t, 0xAB, nil)
	fw := makeForwarder(false, nil, nil) // proxy-seq off: ShardSeqNum stays 0
	conn, _ := openLoopbackUDP(t)
	buf := encodeBuf()
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)

	// buf should now contain a v2-encoded frame (the WriteTo to multicast
	// fails on loopback, but Encode was called into buf before WriteTo).
	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.Version != frame.FrameVerV2 {
		t.Errorf("egress Version = 0x%02X, want 0x%02X (v2)", got.Version, frame.FrameVerV2)
	}
	if got.TxID[0] != 0xAB {
		t.Errorf("TxID[0] = 0x%02X, want 0xAB", got.TxID[0])
	}
}

func TestProcessV1FrameWithProxySeq(t *testing.T) {
	// v1 frame + proxy-seq enabled: ShardSeqNum should be stamped.
	raw := buildV1Frame(t, 0xAB, nil)
	fw := makeForwarder(true, nil, nil) // proxy-seq on
	conn, _ := openLoopbackUDP(t)
	buf := encodeBuf()
	fw.Process(makeTargets(t, conn), buf, raw, fakeAddr{}, 0)

	got, err := frame.Decode(buf[:frame.HeaderSize])
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got.ShardSeqNum != 0 {
		t.Errorf("first v1 frame seq = %d, want 0 (first counter value)", got.ShardSeqNum)
	}
}

func TestProcessDebugMode(t *testing.T) {
	engine := shard.New(0xFF05, [11]byte{}, 8)
	counters := sequence.NewCounters(engine.NumGroups())
	fw := New(engine, counters, 9001, false, nil, nil, true, nil)
	raw := buildV2Frame(t, 0xAB, 1, 0, nil)
	conn, _ := openLoopbackUDP(t)
	fw.Process(makeTargets(t, conn), encodeBuf(), raw, fakeAddr{}, 0)
}

// ── OpenTargets / CloseTargets ────────────────────────────────────────────────

func realIface(t *testing.T) *net.Interface {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("no network interfaces available")
	}
	return &ifaces[0]
}

func TestOpenAndCloseTargets(t *testing.T) {
	iface := realIface(t)
	fw := makeForwarder(false, nil, nil)
	targets, err := fw.OpenTargets([]*net.Interface{iface}, false)
	if err != nil {
		t.Skipf("OpenTargets(%s): %v", iface.Name, err)
	}
	if len(targets) != 1 {
		t.Errorf("len(targets) = %d, want 1", len(targets))
	}
	CloseTargets(targets, slog.Default())
}

func TestOpenTargetsEmpty(t *testing.T) {
	fw := makeForwarder(false, nil, nil)
	targets, err := fw.OpenTargets(nil, false)
	if err != nil {
		t.Errorf("OpenTargets(nil): unexpected error: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("expected 0 targets for nil ifaces, got %d", len(targets))
	}
}

func TestOpenTargetsProbe(t *testing.T) {
	iface := realIface(t)
	fw := makeForwarder(false, nil, nil)
	targets, err := fw.OpenTargets([]*net.Interface{iface}, true)
	if err != nil {
		t.Skipf("OpenTargets probe(%s): %v", iface.Name, err)
	}
	CloseTargets(targets, slog.Default())
}

// ── isErrno ───────────────────────────────────────────────────────────────────

func TestIsErrnoMatch(t *testing.T) {
	if !isErrno(syscall.EPERM, syscall.EPERM) {
		t.Error("isErrno should match EPERM directly")
	}
}

func TestIsErrnoNoMatch(t *testing.T) {
	if isErrno(syscall.EPERM, syscall.EBADF) {
		t.Error("isErrno should not match EBADF when err is EPERM")
	}
}

func TestIsErrnoNil(t *testing.T) {
	if isErrno(nil, syscall.EPERM) {
		t.Error("isErrno(nil) should return false")
	}
}

func TestIsErrnoWrapped(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", syscall.EACCES)
	if !isErrno(err, syscall.EACCES) {
		t.Error("isErrno should match wrapped errno")
	}
}

// ── probeEgressSocket ─────────────────────────────────────────────────────────

func TestProbeEgressSocketLoopback(t *testing.T) {
	conn, _ := openLoopbackUDP(t)
	iface := &net.Interface{Index: 1, Name: "lo"}
	if err := probeEgressSocket(slog.Default(), conn, iface); err != nil {
		t.Errorf("probeEgressSocket on loopback: unexpected hard error: %v", err)
	}
}

func TestProbeEgressSocketClosedConn(t *testing.T) {
	conn, _ := openLoopbackUDP(t)
	iface := &net.Interface{Index: 1, Name: "lo"}
	conn.Close()
	_ = probeEgressSocket(slog.Default(), conn, iface)
}
