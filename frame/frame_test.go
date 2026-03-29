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

func TestRoundTrip(t *testing.T) {
	payload := []byte("fake-bsv-tx-payload")
	f := makeFrame(0xAB, payload)
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
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, payload)
	}
}

func TestDecodeErrTooShort(t *testing.T) {
	_, err := Decode(make([]byte, HeaderSize-1))
	if err != ErrTooShort {
		t.Errorf("want ErrTooShort, got %v", err)
	}
}

func TestDecodeErrBadMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	// Leave magic as 0x00000000 — does not match MagicBSV.
	_, err := Decode(buf)
	if err == nil {
		t.Fatal("want error for bad magic, got nil")
	}
}

func TestDecodeErrBadVer(t *testing.T) {
	buf := make([]byte, HeaderSize)
	// Write correct magic, wrong frame version.
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

func TestHeaderSize(t *testing.T) {
	if HeaderSize != 44 {
		t.Errorf("HeaderSize = %d, want 44", HeaderSize)
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
	buf[6] = FrameVer
	// Write a payLen that exceeds MaxPayload into bytes 40–43.
	binary.BigEndian.PutUint32(buf[40:44], uint32(MaxPayload+1))
	_, err := Decode(buf)
	if err != ErrTooLarge {
		t.Errorf("want ErrTooLarge, got %v", err)
	}
}
