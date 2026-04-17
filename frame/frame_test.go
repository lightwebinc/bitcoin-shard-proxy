package frame

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func makeFrame(txidByte0 byte, payload []byte) *Frame {
	f := &Frame{Payload: payload}
	f.TxID[0] = txidByte0
	return f
}

// ── Constants ─────────────────────────────────────────────────────────────────

func TestHeaderSize(t *testing.T) {
	if HeaderSize != 100 {
		t.Errorf("HeaderSize = %d, want 100", HeaderSize)
	}
}

func TestHeaderSizeV1(t *testing.T) {
	if HeaderSizeV1 != 44 {
		t.Errorf("HeaderSizeV1 = %d, want 44", HeaderSizeV1)
	}
}

func TestFrameVerV1(t *testing.T) {
	if FrameVerV1 != 0x01 {
		t.Errorf("FrameVerV1 = 0x%02X, want 0x01", FrameVerV1)
	}
}

func TestFrameVerV2(t *testing.T) {
	if FrameVerV2 != 0x02 {
		t.Errorf("FrameVerV2 = 0x%02X, want 0x02", FrameVerV2)
	}
}

// ── Round-trip (all fields) ───────────────────────────────────────────────────

func TestRoundTrip(t *testing.T) {
	payload := []byte("fake-bsv-tx-payload")
	f := &Frame{
		Payload:     payload,
		ShardSeqNum: 0x0102030405060708,
	}
	f.TxID[0] = 0xAB
	for i := range f.SubtreeID {
		f.SubtreeID[i] = byte(i + 1)
	}

	buf := make([]byte, HeaderSize+len(payload))
	n, err := Encode(f, buf)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if n != HeaderSize+len(payload) {
		t.Fatalf("Encode returned %d bytes, want %d", n, HeaderSize+len(payload))
	}

	got, err := Decode(buf[:n])
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.TxID != f.TxID {
		t.Errorf("TxID mismatch: got %x, want %x", got.TxID, f.TxID)
	}
	if got.ShardSeqNum != f.ShardSeqNum {
		t.Errorf("ShardSeqNum = %d, want %d", got.ShardSeqNum, f.ShardSeqNum)
	}
	if got.SubtreeID != f.SubtreeID {
		t.Errorf("SubtreeID mismatch")
	}
	if got.SenderID != f.SenderID {
		t.Errorf("SenderID mismatch: got %x, want %x", got.SenderID, f.SenderID)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, payload)
	}
}

func TestRoundTripWithSenderID(t *testing.T) {
	payload := []byte("tx-with-sender")
	f := &Frame{
		Payload:     payload,
		ShardSeqNum: 42,
	}
	f.TxID[0] = 0xCC
	for i := range f.SenderID {
		f.SenderID[i] = byte(0x20 + i)
	}

	buf := make([]byte, HeaderSize+len(payload))
	if _, err := Encode(f, buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.SenderID != f.SenderID {
		t.Errorf("SenderID mismatch: got %x, want %x", got.SenderID, f.SenderID)
	}
}

// ── Field offsets ─────────────────────────────────────────────────────────────

func TestFieldOffsets(t *testing.T) {
	f := &Frame{
		ShardSeqNum: 0xDEADBEEFCAFEBABE,
	}
	f.TxID[0] = 0x11
	for i := range f.SubtreeID {
		f.SubtreeID[i] = 0xCC
	}
	f.Payload = []byte{0xFF}

	buf := make([]byte, HeaderSize+1)
	if _, err := Encode(f, buf); err != nil {
		t.Fatal(err)
	}

	if buf[6] != FrameVerV2 {
		t.Errorf("buf[6] (FrameVer) = 0x%02X, want 0x%02X", buf[6], FrameVerV2)
	}
	if buf[7] != 0 {
		t.Errorf("buf[7] (Reserved) = 0x%02X, want 0x00", buf[7])
	}
	if buf[8] != 0x11 {
		t.Errorf("buf[8] (TxID[0]) = 0x%02X, want 0x11", buf[8])
	}
	if binary.BigEndian.Uint64(buf[40:48]) != 0xDEADBEEFCAFEBABE {
		t.Errorf("buf[40:48] (ShardSeqNum) mismatch")
	}
	if buf[48] != 0xCC {
		t.Errorf("buf[48] (SubtreeID[0]) = 0x%02X, want 0xCC", buf[48])
	}
	for i := 80; i < 96; i++ {
		if buf[i] != 0x00 {
			t.Errorf("buf[%d] (SenderID[%d]) = 0x%02X, want 0x00", i, i-80, buf[i])
		}
	}
	payLen := binary.BigEndian.Uint32(buf[96:100])
	if payLen != 1 {
		t.Errorf("buf[96:100] (PayLen) = %d, want 1", payLen)
	}
	if buf[100] != 0xFF {
		t.Errorf("buf[100] (Payload[0]) = 0x%02X, want 0xFF", buf[100])
	}
}

// ── Empty payload ─────────────────────────────────────────────────────────────

func TestEmptyPayload(t *testing.T) {
	f := makeFrame(0x77, []byte{})
	buf := make([]byte, HeaderSize)
	n, err := Encode(f, buf)
	if err != nil {
		t.Fatalf("Encode empty payload: %v", err)
	}
	if n != HeaderSize {
		t.Errorf("n = %d, want %d", n, HeaderSize)
	}
	got, err := Decode(buf[:n])
	if err != nil {
		t.Fatalf("Decode empty payload: %v", err)
	}
	if len(got.Payload) != 0 {
		t.Errorf("Payload len = %d, want 0", len(got.Payload))
	}
}

// ── v1 frame decode ───────────────────────────────────────────────────────────

// buildV1Frame assembles a minimal valid v1 datagram.
func buildV1Frame(txidByte byte, payload []byte) []byte {
	buf := make([]byte, HeaderSizeV1+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], MagicBSV)
	binary.BigEndian.PutUint16(buf[4:6], ProtoVer)
	buf[6] = FrameVerV1
	// buf[7] = 0x00 (reserved)
	buf[8] = txidByte
	binary.BigEndian.PutUint32(buf[40:44], uint32(len(payload)))
	copy(buf[44:], payload)
	return buf
}

func TestDecodeV1Basic(t *testing.T) {
	payload := []byte("v1-tx-payload")
	raw := buildV1Frame(0xAB, payload)
	f, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode v1: %v", err)
	}
	if f.Version != FrameVerV1 {
		t.Errorf("Version = 0x%02X, want 0x%02X", f.Version, FrameVerV1)
	}
	if f.TxID[0] != 0xAB {
		t.Errorf("TxID[0] = 0x%02X, want 0xAB", f.TxID[0])
	}
	if !bytes.Equal(f.Payload, payload) {
		t.Errorf("Payload mismatch: got %q, want %q", f.Payload, payload)
	}
}

func TestDecodeV1ZeroedV2Fields(t *testing.T) {
	raw := buildV1Frame(0x01, nil)
	f, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode v1: %v", err)
	}
	if f.ShardSeqNum != 0 {
		t.Errorf("ShardSeqNum = %d, want 0", f.ShardSeqNum)
	}
	if f.SubtreeID != ([32]byte{}) {
		t.Error("SubtreeID should be all zeros for v1")
	}
	if f.SenderID != ([16]byte{}) {
		t.Error("SenderID should be all zeros for v1")
	}
}

func TestDecodeV1EmptyPayload(t *testing.T) {
	raw := buildV1Frame(0x77, nil)
	f, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode v1 empty payload: %v", err)
	}
	if len(f.Payload) != 0 {
		t.Errorf("Payload len = %d, want 0", len(f.Payload))
	}
}

func TestDecodeV1Truncated(t *testing.T) {
	raw := buildV1Frame(0x01, []byte("hello"))
	_, err := Decode(raw[:len(raw)-1])
	if err != io.ErrUnexpectedEOF {
		t.Errorf("want io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestDecodeV1TooLarge(t *testing.T) {
	buf := make([]byte, HeaderSizeV1)
	binary.BigEndian.PutUint32(buf[0:4], MagicBSV)
	buf[6] = FrameVerV1
	binary.BigEndian.PutUint32(buf[40:44], uint32(MaxPayload+1))
	_, err := Decode(buf)
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

// ── Error paths ───────────────────────────────────────────────────────────────

func TestDecodeErrTooShort(t *testing.T) {
	// Shorter than even the v1 header
	_, err := Decode(make([]byte, HeaderSizeV1-1))
	if err != ErrTooShort {
		t.Errorf("want ErrTooShort, got %v", err)
	}
}

func TestDecodeV2ErrTooShort(t *testing.T) {
	// Long enough for v1 but not v2
	buf := make([]byte, HeaderSizeV1)
	binary.BigEndian.PutUint32(buf[0:4], MagicBSV)
	buf[6] = FrameVerV2
	_, err := Decode(buf)
	if err != ErrTooShort {
		t.Errorf("want ErrTooShort for v2 with only %d bytes, got %v", HeaderSizeV1, err)
	}
}

func TestDecodeErrBadMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	_, err := Decode(buf)
	if err == nil {
		t.Fatal("want error for bad magic, got nil")
	}
}

func TestDecodeErrBadVer(t *testing.T) {
	buf := make([]byte, HeaderSize)
	buf[0], buf[1], buf[2], buf[3] = 0xE3, 0xE1, 0xF3, 0xE8
	buf[6] = 0xFF
	_, err := Decode(buf)
	if err == nil {
		t.Fatal("want error for bad frame version, got nil")
	}
}

func TestDecodeErrTruncated(t *testing.T) {
	f := makeFrame(0x01, []byte("payload"))
	buf := make([]byte, HeaderSize+len(f.Payload))
	n, _ := Encode(f, buf)
	_, err := Decode(buf[:n-1])
	if err != io.ErrUnexpectedEOF {
		t.Errorf("want io.ErrUnexpectedEOF, got %v", err)
	}
}

func TestEncodeBufferTooSmall(t *testing.T) {
	f := makeFrame(0x01, []byte("payload"))
	_, err := Encode(f, make([]byte, 1))
	if err == nil {
		t.Fatal("want error for buffer too small, got nil")
	}
}

func TestEncodeErrTooLarge(t *testing.T) {
	f := &Frame{Payload: make([]byte, MaxPayload+1)}
	_, err := Encode(f, make([]byte, HeaderSize+MaxPayload+1))
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}

func TestDecodeErrTooLarge(t *testing.T) {
	buf := make([]byte, HeaderSize)
	buf[0], buf[1], buf[2], buf[3] = 0xE3, 0xE1, 0xF3, 0xE8
	buf[6] = FrameVerV2
	// Write a payLen that exceeds MaxPayload into bytes 96–99.
	binary.BigEndian.PutUint32(buf[96:100], uint32(MaxPayload+1))
	_, err := Decode(buf)
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}
