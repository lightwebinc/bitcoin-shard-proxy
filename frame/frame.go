// Package frame defines the BSV-over-UDP wire format (v2) used by
// bitcoin-shard-proxy.
//
// # Wire format — v2 (84 bytes, zero padding, all multi-byte fields 8-byte aligned)
//
// All multi-byte integers are big-endian.
//
//	Offset  Size  Align  Field            Value / notes
//	------  ----  -----  -----            -------------
//	     0     4   —     Network magic    0xE3E1F3E8  (BSV mainnet P2P magic)
//	     4     2   —     Protocol ver     0x02BF = 703 (BSV node version baseline)
//	     6     1   —     Frame version    0x02
//	     7     1   1B    Subtree height   uint8; log₂(subtree capacity); 0 = unset
//	     8    32   8B    Transaction ID   raw 256-bit txid (NOT display-reversed)
//	    40     8   8B    Shard seq num    uint64 BE; sender-assigned or proxy fallback; 0 = unset
//	    48    32   8B    Subtree ID       32-byte batch identifier assigned by tx processor; zeros = unset
//	    80     4   8B    Payload length   uint32; max [MaxPayload] bytes
//	    84     *   4B    BSV tx payload   raw serialised transaction bytes
//
// The txid at offset 8 is in internal byte order (as used in the BSV P2P
// protocol and raw transaction data), not the reversed display order shown
// by block explorers.
//
// # v1 compatibility
//
// v1 frames (FrameVer = 0x01, 44-byte header) are rejected by [Decode] with
// [ErrBadVer]. All senders must use v2. The v1 constant [FrameVerV1] is
// exported only to produce informative error messages.
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

	// FrameVerV1 is the legacy v1 frame version. Frames with this version
	// are rejected; the constant is exported for diagnostic messages only.
	FrameVerV1 byte = 0x01

	// FrameVerV2 is the current frame version.
	FrameVerV2 byte = 0x02

	// HeaderSize is the fixed size of the v2 frame header in bytes.
	// Kept as HeaderSize (not HeaderSizeV2) so callers need no rename.
	HeaderSize = 84

	// MaxPayload is the maximum accepted payload size. BSV's consensus rule
	// caps individual transactions well below this; the limit guards against
	// malformed or malicious frames consuming excessive memory.
	MaxPayload = 10 * 1024 * 1024 // 10 MiB
)

// Sentinel errors returned by [Decode].
var (
	// ErrBadMagic is returned when the first four bytes do not match MagicBSV.
	ErrBadMagic = errors.New("frame: invalid BSV magic bytes")

	// ErrBadVer is returned when the frame version byte is not FrameVerV2.
	// This includes v1 frames (0x01), which are no longer accepted.
	ErrBadVer = errors.New("frame: unsupported frame version")

	// ErrTooLarge is returned when the payload length field exceeds MaxPayload.
	ErrTooLarge = errors.New("frame: payload length exceeds MaxPayload")

	// ErrTooShort is returned when the datagram is shorter than HeaderSize.
	ErrTooShort = errors.New("frame: datagram shorter than header")
)

// Frame is the parsed in-memory representation of a v2 BSV datagram.
//
// Payload is a zero-copy slice pointing into the buffer passed to [Decode];
// the buffer must remain valid for the lifetime of the Frame.
type Frame struct {
	TxID          [32]byte // Raw 256-bit transaction ID (internal byte order)
	ShardSeqNum   uint64   // Monotonic sequence number; 0 = unset
	SubtreeID     [32]byte // 32-byte batch identifier assigned by tx processor; zeros = unset
	SubtreeHeight uint8    // log₂(subtree capacity); 0 = unset
	Payload       []byte   // Raw serialised BSV transaction
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
	buf[6] = FrameVerV2
	buf[7] = f.SubtreeHeight
	copy(buf[8:40], f.TxID[:])
	binary.BigEndian.PutUint64(buf[40:48], f.ShardSeqNum)
	copy(buf[48:80], f.SubtreeID[:])
	binary.BigEndian.PutUint32(buf[80:84], uint32(len(f.Payload)))
	copy(buf[84:], f.Payload)

	return total, nil
}

// Decode parses a raw v2 datagram into a Frame.
//
// The returned Frame.Payload is a zero-copy slice into buf. The caller must
// not modify or reuse buf while the Frame is in scope.
//
// v1 frames (FrameVer 0x01) are rejected with [ErrBadVer].
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

	if fver := buf[6]; fver != FrameVerV2 {
		return nil, fmt.Errorf("%w: got 0x%02X", ErrBadVer, fver)
	}

	payLen := int(binary.BigEndian.Uint32(buf[80:84]))
	if payLen > MaxPayload {
		return nil, ErrTooLarge
	}

	if len(buf)-HeaderSize < payLen {
		return nil, io.ErrUnexpectedEOF
	}

	f := &Frame{}
	f.SubtreeHeight = buf[7]
	copy(f.TxID[:], buf[8:40])
	f.ShardSeqNum = binary.BigEndian.Uint64(buf[40:48])
	copy(f.SubtreeID[:], buf[48:80])
	f.Payload = buf[HeaderSize : HeaderSize+payLen]
	return f, nil
}
