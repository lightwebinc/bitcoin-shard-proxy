// Package sequence provides per-shard monotonic sequence counters for
// bitcoin-shard-proxy. Each counter is an independent atomic uint64 that
// increments without locking, giving O(1) Next calls with no contention
// between shards.
package sequence

import "sync/atomic"

// Counters holds one atomic counter per shard group.
// Create with [NewCounters]; safe for concurrent use by multiple goroutines.
type Counters struct {
	slots []atomic.Uint64
}

// NewCounters allocates a Counters with numShards independent counters, all
// starting at zero. numShards should equal shard.Engine.NumGroups().
func NewCounters(numShards uint32) *Counters {
	return &Counters{slots: make([]atomic.Uint64, numShards)}
}

// Next atomically increments the counter for shardIdx and returns the value
// before the increment. The first call returns 0, the second 1, and so on.
//
// shardIdx must be in [0, numShards). Behaviour is undefined if it is out of
// range (index panic).
func (c *Counters) Next(shardIdx uint32) uint64 {
	return c.slots[shardIdx].Add(1) - 1
}
