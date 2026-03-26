// Package frame defines the BSV-over-UDP wire format used by
// bitcoin-shard-proxy.
//
// # Wire format
//
// All multi-byte integers are big-endian. The frame is designed to be
// identifiable by existing BSV tooling and firewalls because its first four
// bytes match the BSV mainnet P2P network magic.
//
//	Offset  Size  Field           Value / notes
//	------  ----  -----           -------------
//	     0     4  Network magic   0xE3E1F3E8  (BSV mainnet P2P magic)
//	     4     2  Protocol ver    0x02BF = 703 (BSV node version baseline)
//	     6     1  Frame version   0x01
//	     7     1  Reserved        0x00
//	     8    32  Transaction ID  raw 256-bit txid (NOT display-reversed)
//	    40     4  Payload length  uint32; max [MaxPayload] bytes
//	    44     *  BSV tx payload  raw serialised transaction bytes
//
// The txid at offset 8 is in internal byte order (as used in the BSV P2P
// protocol and raw transaction data), not the reversed display order shown
// by block explorers.
//
// # BSV transaction format compatibility
//
// The payload field carries a raw BSV transaction in the same serialisation
// format as the BSV P2P "tx" message payload: version (4 bytes LE), input
// vector, output vector, locktime (4 bytes LE). No P2P message envelope
// wraps it — the frame header above serves that role.
package frame

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Wire format constants.
const (
	// MagicBSV is the BSV mainnet P2P network magic, used as the frame
	// identifier. Matches the first four bytes of every BSV P2P message.
	MagicBSV uint32 = 0xE3E1F3E8

	// ProtoVer is the protocol version field. 703 (0x02BF) is the BSV node
	// version baseline that introduced the large-block policy.
	ProtoVer uint16 = 0x02BF

	// FrameVer is the frame format version. Increment if the header layout
	// changes incompatibly.
	FrameVer byte = 0x01

	// HeaderSize is the fixed size of the frame header in bytes.
	HeaderSize = 4 + 2 + 1 + 1 + 32 + 4 // 44 bytes

	// MaxPayload is the maximum accepted payload size. BSV's consensus rule
	// caps individual transactions well below this; the limit guards against
	// malformed or malicious frames consuming excessive memory.
	MaxPayload = 10 * 1024 * 1024 // 10 MiB
)

// Sentinel errors returned by [Decode].
var (
	// ErrBadMagic is returned when the first four bytes do not match MagicBSV.
	ErrBadMagic = errors.New("frame: invalid BSV magic bytes")

	// ErrBadVer is returned when the frame version byte is not FrameVer.
	ErrBadVer = errors.New("frame: unsupported frame version")

	// ErrTooLarge is returned when the payload length field exceeds MaxPayload.
	ErrTooLarge = errors.New("frame: payload length exceeds MaxPayload")

	// ErrTooShort is returned when the datagram is shorter than HeaderSize.
	ErrTooShort = errors.New("frame: datagram shorter than header")
)

// Frame is the parsed in-memory representation of a BSV-over-UDP datagram.
//
// Payload is a zero-copy slice pointing into the buffer passed to [Decode];
// the buffer must remain valid for the lifetime of the Frame.
type Frame struct {
	TxID    [32]byte // Raw 256-bit transaction ID (internal byte order)
	Payload []byte   // Raw serialised BSV transaction
}

// Encode serialises f into buf and returns the number of bytes written.
// buf must be at least HeaderSize + len(f.Payload) bytes long.
//
// Returns an error if buf is too small or the payload exceeds [MaxPayload].
func Encode(f *Frame, buf []byte) (int, error) {
	if len(f.Payload) > MaxPayload {
		return 0, ErrTooLarge
	}
	total := HeaderSize + len(f.Payload)
	if len(buf) < total {
		return 0, fmt.Errorf("frame: buffer too small (%d bytes, need %d)", len(buf), total)
	}

	binary.BigEndian.PutUint32(buf[0:4], MagicBSV)
	binary.BigEndian.PutUint16(buf[4:6], ProtoVer)
	buf[6] = FrameVer
	buf[7] = 0x00
	copy(buf[8:40], f.TxID[:])
	binary.BigEndian.PutUint32(buf[40:44], uint32(len(f.Payload)))
	copy(buf[44:], f.Payload)

	return total, nil
}

// Decode parses a raw UDP datagram into a Frame.
//
// The returned Frame.Payload is a zero-copy slice into buf. The caller must
// not modify or reuse buf while the Frame is in scope.
//
// Possible errors: [ErrTooShort], [ErrBadMagic], [ErrBadVer], [ErrTooLarge],
// or [io.ErrUnexpectedEOF] if the datagram is truncated relative to the
// declared payload length.
func Decode(buf []byte) (*Frame, error) {
	if len(buf) < HeaderSize {
		return nil, ErrTooShort
	}

	if magic := binary.BigEndian.Uint32(buf[0:4]); magic != MagicBSV {
		return nil, fmt.Errorf("%w: got 0x%08X", ErrBadMagic, magic)
	}

	if fver := buf[6]; fver != FrameVer {
		return nil, fmt.Errorf("%w: got 0x%02X", ErrBadVer, fver)
	}

	payLen := int(binary.BigEndian.Uint32(buf[40:44]))
	if payLen > MaxPayload {
		return nil, ErrTooLarge
	}

	available := len(buf) - HeaderSize
	if available < payLen {
		return nil, io.ErrUnexpectedEOF
	}

	f := &Frame{}
	copy(f.TxID[:], buf[8:40])
	f.Payload = buf[HeaderSize : HeaderSize+payLen]
	return f, nil
}
