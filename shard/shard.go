// Package shard derives IPv6 multicast group addresses from BSV transaction
// IDs for deterministic packet-level sharding across the BSV sharding pipeline.
//
// # Sharding strategy
//
// The derivation is pure arithmetic: no allocation, no locks, safe for
// concurrent use by multiple goroutines without synchronisation.
//
// Given a 256-bit txid and a configured bit width N (1–24), the group index
// is the top N bits of the first 32-bit word of the txid:
//
//	groupIndex = (txid[0:4] as uint32) >> (32 - N)
//
// Using the top bits — rather than a modulo operation on the bottom bits —
// gives consistent-hashing behaviour: when N increases by 1, every existing
// group splits into exactly two child groups. Subscribers only need to join
// additional groups; no existing subscriptions become invalid.
//
// # Address layout
//
//	bits [127:112]   FFsc   multicast prefix + scope nibble  (16 bits)
//	bits [111:24]    0x00   zero padding                     (88 bits)
//	bits [23:0]      index  group index                      (24 bits max)
//
// The group index is placed in the three lowest bytes of the address
// (bytes 13–15), allowing up to 24-bit shard spaces (16,777,216 groups).
package shard

import (
	"encoding/binary"
	"net"
)

// Engine holds the immutable sharding parameters. Construct one with [New]
// and share it freely across goroutines.
type Engine struct {
	mcPrefix    uint16   // upper 16 bits of the IPv6 multicast address, e.g. 0xFF05
	middleBytes [11]byte // bytes 2-12 of the IPv6 address (assigned address space)
	shardBits   uint     // number of txid prefix bits used as the group key
	mask        uint32   // (1 << shardBits) - 1; applied after the shift
}

// New creates a shard Engine.
//
//   - mcPrefix is the two-byte IPv6 multicast prefix (e.g. 0xFF05 for
//     site-local scope). Use the values in [config.Scopes].
//   - middleBytes are bytes 2-12 of the IPv6 address for assigned address space.
//   - shardBits is the number of bits from the txid prefix that form the
//     group key. Must be in [1, 24].
func New(mcPrefix uint16, middleBytes [11]byte, shardBits uint) *Engine {
	return &Engine{
		mcPrefix:    mcPrefix,
		middleBytes: middleBytes,
		shardBits:   shardBits,
		mask:        (1 << shardBits) - 1,
	}
}

// GroupIndex returns the shard group index for a given txid.
//
// Only the first four bytes of txid are examined. The top shardBits bits
// of those four bytes are extracted as a big-endian unsigned integer,
// producing a value in [0, NumGroups).
//
// This function is safe for concurrent use without synchronisation.
func (e *Engine) GroupIndex(txid *[32]byte) uint32 {
	prefix32 := binary.BigEndian.Uint32(txid[0:4])
	return (prefix32 >> (32 - e.shardBits)) & e.mask
}

// Addr constructs the 16-byte IPv6 multicast [net.UDPAddr] for the given
// group index and destination port.
//
// The returned address is a newly allocated value on each call; callers may
// cache the result if the group index and port are stable.
func (e *Engine) Addr(groupIndex uint32, port int) *net.UDPAddr {
	ip := make(net.IP, 16)
	binary.BigEndian.PutUint16(ip[0:2], e.mcPrefix)
	copy(ip[2:13], e.middleBytes[:]) // Convert array to slice with [:]
	ip[13] = byte(groupIndex >> 16)
	ip[14] = byte(groupIndex >> 8)
	ip[15] = byte(groupIndex)
	return &net.UDPAddr{IP: ip, Port: port}
}

// ShardBits returns the configured bit width for informational and logging use.
func (e *Engine) ShardBits() uint { return e.shardBits }

// NumGroups returns the total number of distinct multicast groups in the
// configured shard space (2^ShardBits).
func (e *Engine) NumGroups() uint32 { return e.mask + 1 }
