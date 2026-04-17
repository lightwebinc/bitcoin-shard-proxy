// Package frame defines the BSV-over-UDP v1 and v2 wire formats used by
// bitcoin-shard-proxy.
//
// # Wire format — v1 (44 bytes, legacy)
//
// All multi-byte integers are big-endian.
//
//	Offset  Size  Field            Value / notes
//	------  ----  -----            -------------
//	     0     4  Network magic    0xE3E1F3E8
//	     4     2  Protocol ver     0x02BF
//	     6     1  Frame version    0x01
//	     7     1  Reserved         0x00
//	     8    32  Transaction ID   raw 256-bit txid
//	    40     4  Payload length   uint32; max [MaxPayload] bytes
//	    44     *  BSV tx payload
//
// # Wire format — v2 (100 bytes, zero padding, all multi-byte fields 8-byte aligned)
//
//	Offset  Size  Align  Field            Value / notes
//	------  ----  -----  -----            -------------
//	     0     4   —     Network magic    0xE3E1F3E8  (BSV mainnet P2P magic)
//	     4     2   —     Protocol ver     0x02BF = 703 (BSV node version baseline)
//	     6     1   —     Frame version    0x02
//	     7     1   —     Reserved         0x00
//	     8    32   8B    Transaction ID   raw 256-bit txid (NOT display-reversed)
//	    40     8   8B    Shard seq num    uint64 BE; sender-assigned; 0 = unset
//	    48    32   8B    Subtree ID       32-byte batch identifier assigned by tx processor; zeros = unset
//	    80    16   8B    Sender ID        original BSV sender IPv6 address (net.IP.To16()); zeros = unset
//	    96     4   4B    Payload length   uint32; max [MaxPayload] bytes
//	   100     *   —     BSV tx payload   raw serialised transaction bytes
//
// The txid at offset 8 is in internal byte order (as used in the BSV P2P
// protocol and raw transaction data), not the reversed display order shown
// by block explorers.
//
// # v1 handling
//
// [Decode] accepts both v1 and v2 frames. v1 frames are decoded into a [Frame]
// with [Version] = [FrameVerV1] and zero-valued [ShardSeqNum], [SubtreeID],
// and [SenderID]. The forwarder forwards v1 frames verbatim (no re-encoding).
// Unknown versions return [ErrBadVer].
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

	// FrameVerV1 is the legacy v1 frame version (44-byte header). [Decode]
	// accepts v1 frames and returns them with zero-valued v2-only fields.
	FrameVerV1 byte = 0x01

	// FrameVerV2 is the current frame version.
	FrameVerV2 byte = 0x02

	// HeaderSizeV1 is the fixed size of the v1 frame header in bytes.
	HeaderSizeV1 = 44

	// HeaderSize is the fixed size of the v2 frame header in bytes.
	// Kept as HeaderSize (not HeaderSizeV2) so callers need no rename.
	HeaderSize = 100

	// MaxPayload is the maximum accepted payload size. BSV's consensus rule
	// caps individual transactions well below this; the limit guards against
	// malformed or malicious frames consuming excessive memory.
	MaxPayload = 10 * 1024 * 1024 // 10 MiB
)

// Sentinel errors returned by [Decode].
var (
	// ErrBadMagic is returned when the first four bytes do not match MagicBSV.
	ErrBadMagic = errors.New("frame: invalid BSV magic bytes")

	// ErrBadVer is returned when the frame version byte is neither FrameVerV1
	// nor FrameVerV2.
	ErrBadVer = errors.New("frame: unsupported frame version")

	// ErrTooLarge is returned when the payload length field exceeds MaxPayload.
	ErrTooLarge = errors.New("frame: payload length exceeds MaxPayload")

	// ErrTooShort is returned when the datagram is shorter than the minimum
	// header size ([HeaderSizeV1] for v1, [HeaderSize] for v2).
	ErrTooShort = errors.New("frame: datagram shorter than header")
)

// Frame is the parsed in-memory representation of a v1 or v2 BSV datagram.
//
// Payload is a zero-copy slice pointing into the buffer passed to [Decode];
// the buffer must remain valid for the lifetime of the Frame.
type Frame struct {
	Version     byte     // FrameVerV1 or FrameVerV2 — set by Decode
	TxID        [32]byte // Raw 256-bit transaction ID (internal byte order)
	ShardSeqNum uint64   // Monotonic sequence number; 0 = unset (always 0 for v1)
	SubtreeID   [32]byte // 32-byte batch identifier; zeros = unset (always zero for v1)
	SenderID    [16]byte // Original BSV sender IPv6 address (net.IP.To16()); zeros = unset (always zero for v1)
	Payload     []byte   // Raw serialised BSV transaction
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
	buf[7] = 0
	copy(buf[8:40], f.TxID[:])
	binary.BigEndian.PutUint64(buf[40:48], f.ShardSeqNum)
	copy(buf[48:80], f.SubtreeID[:])
	copy(buf[80:96], f.SenderID[:])
	binary.BigEndian.PutUint32(buf[96:100], uint32(len(f.Payload)))
	copy(buf[100:], f.Payload)

	return total, nil
}

// Decode parses a raw v1 or v2 datagram into a Frame.
//
// The returned Frame.Payload is a zero-copy slice into buf. The caller must
// not modify or reuse buf while the Frame is in scope.
//
// v1 frames (FrameVer 0x01) are decoded with [Version] = [FrameVerV1] and
// zero-valued [ShardSeqNum], [SubtreeID], and [SenderID]. The forwarder
// forwards v1 frames verbatim (no re-encoding).
//
// Unknown versions return [ErrBadVer].
//
// Possible errors: [ErrTooShort], [ErrBadMagic], [ErrBadVer], [ErrTooLarge],
// or [io.ErrUnexpectedEOF] if the datagram is truncated relative to the
// declared payload length.
func Decode(buf []byte) (*Frame, error) {
	if len(buf) < HeaderSizeV1 {
		return nil, ErrTooShort
	}

	if magic := binary.BigEndian.Uint32(buf[0:4]); magic != MagicBSV {
		return nil, fmt.Errorf("%w: got 0x%08X", ErrBadMagic, magic)
	}

	fver := buf[6]
	switch fver {
	case FrameVerV1:
		return decodeV1(buf)
	case FrameVerV2:
		return decodeV2(buf)
	default:
		return nil, fmt.Errorf("%w: got 0x%02X", ErrBadVer, fver)
	}
}

// decodeV1 parses the 44-byte v1 header. New v2 fields default to zero.
func decodeV1(buf []byte) (*Frame, error) {
	if len(buf) < HeaderSizeV1 {
		return nil, ErrTooShort
	}
	payLen := int(binary.BigEndian.Uint32(buf[40:44]))
	if payLen > MaxPayload {
		return nil, ErrTooLarge
	}
	if len(buf)-HeaderSizeV1 < payLen {
		return nil, io.ErrUnexpectedEOF
	}
	f := &Frame{Version: FrameVerV1}
	copy(f.TxID[:], buf[8:40])
	f.Payload = buf[HeaderSizeV1 : HeaderSizeV1+payLen]
	return f, nil
}

// decodeV2 parses the 100-byte v2 header.
func decodeV2(buf []byte) (*Frame, error) {
	if len(buf) < HeaderSize {
		return nil, ErrTooShort
	}
	payLen := int(binary.BigEndian.Uint32(buf[96:100]))
	if payLen > MaxPayload {
		return nil, ErrTooLarge
	}
	if len(buf)-HeaderSize < payLen {
		return nil, io.ErrUnexpectedEOF
	}
	f := &Frame{Version: FrameVerV2}
	copy(f.TxID[:], buf[8:40])
	f.ShardSeqNum = binary.BigEndian.Uint64(buf[40:48])
	copy(f.SubtreeID[:], buf[48:80])
	copy(f.SenderID[:], buf[80:96])
	f.Payload = buf[HeaderSize : HeaderSize+payLen]
	return f, nil
}
