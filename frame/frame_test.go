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
	if HeaderSize != 84 {
		t.Errorf("HeaderSize = %d, want 84", HeaderSize)
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
		Payload:       payload,
		ShardSeqNum:   0x0102030405060708,
		SubtreeHeight: 20,
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
	if got.SubtreeHeight != f.SubtreeHeight {
		t.Errorf("SubtreeHeight = %d, want %d", got.SubtreeHeight, f.SubtreeHeight)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, payload)
	}
}

// ── Field offsets ─────────────────────────────────────────────────────────────

func TestFieldOffsets(t *testing.T) {
	f := &Frame{
		ShardSeqNum:   0xDEADBEEFCAFEBABE,
		SubtreeHeight: 7,
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
	if buf[7] != 7 {
		t.Errorf("buf[7] (SubtreeHeight) = %d, want 7", buf[7])
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
	payLen := binary.BigEndian.Uint32(buf[80:84])
	if payLen != 1 {
		t.Errorf("buf[80:84] (PayLen) = %d, want 1", payLen)
	}
	if buf[84] != 0xFF {
		t.Errorf("buf[84] (Payload[0]) = 0x%02X, want 0xFF", buf[84])
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

// ── v1 frame rejected ─────────────────────────────────────────────────────────

func TestDecodeRejectsV1(t *testing.T) {
	buf := make([]byte, HeaderSize)
	buf[0], buf[1], buf[2], buf[3] = 0xE3, 0xE1, 0xF3, 0xE8
	buf[6] = FrameVerV1
	_, err := Decode(buf)
	if err == nil {
		t.Fatal("want ErrBadVer for v1 frame, got nil")
	}
}

// ── Error paths ───────────────────────────────────────────────────────────────

func TestDecodeErrTooShort(t *testing.T) {
	_, err := Decode(make([]byte, HeaderSize-1))
	if err != ErrTooShort {
		t.Errorf("want ErrTooShort, got %v", err)
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
	// Write a payLen that exceeds MaxPayload into bytes 80–83.
	binary.BigEndian.PutUint32(buf[80:84], uint32(MaxPayload+1))
	_, err := Decode(buf)
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}
