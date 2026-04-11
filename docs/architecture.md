# Architecture

## Overview

bitcoin-shard-proxy receives BSV v2 transaction frames over UDP (and optionally
TCP), derives a deterministic multicast group address from each transaction's
txid, optionally stamps a proxy-assigned sequence number and static subtree
fields, then retransmits to all configured egress interfaces.

See [docs/protocol.md](protocol.md) for the complete wire format specification.

```text
sender  ──UDP/TCP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>  (iface 0)
                      (forwarder pipeline) └─────────────────►  FF05::<shard>  (iface 1)
                                                                 (subset of subscribers)
```

## Shard Address Derivation

```text
groupIndex = (txid[0:4] as uint32 BE) >> (32 - shardBits)
IPv6 group = [FFsc::groupIndex]       // sc = two-nibble scope code
```

The top bits of the first four bytes of the txid are used as the group key.
Using top bits rather than modulo gives consistent-hashing: when `shardBits`
increases by 1, every existing group splits into exactly two child groups.
Subscribers join additional groups; existing subscriptions remain valid.

## Multi-CPU Design

Each UDP worker goroutine owns one ingress socket bound via `SO_REUSEPORT` plus
one egress socket per configured interface. The kernel distributes incoming
datagrams across all workers with no userspace coordination. Forwarding logic
is centralised in the shared `forwarder.Forwarder`.

### TCP ingress

When `-tcp-listen-port` is non-zero, a single `TCPIngress` goroutine accepts
connections and dispatches each connection to a per-connection goroutine. TCP
and UDP share the same `forwarder.Forwarder` and egress targets.

```
senders (UDP)              proxy (N UDP workers + 1 TCP listener)
─────────────              ─────────────────────────────────────
tx_a  ──UDP──▶ [worker 0] ─▶ forwarder ─▶ FF05::3 ──▶ sub_X
tx_b  ──UDP──▶ [worker 1] ─▶ forwarder ─▶ FF05::1 ──▶ sub_Y
tx_c  ──TCP──▶ [tcp conn] ─▶ forwarder ─▶ FF05::2 ──▶ sub_Z
```

## Wire Format

### v2 (current — 84 bytes, 8-byte aligned, zero padding)

```
Offset  Size  Align  Field
------  ----  -----  -----
     0     4   —     Network magic    0xE3E1F3E8
     4     2   —     Protocol ver     0x02BF
     6     1   —     Frame version    0x02
     7     1   1 B   Subtree height   uint8; 0 = unset
     8    32   8 B   Transaction ID   raw 256-bit txid (internal byte order)
    40     8   8 B   Shard seq num    uint64 BE; 0 = unset
    48    32   8 B   Subtree ID       32-byte batch identifier; zeros = unset
    80     4   8 B   Payload length   uint32 BE
    84     *   —     BSV tx payload
```

### v1 (legacy — rejected)

v1 frames (44-byte header, `FrameVer = 0x01`) are rejected at decode time with
`ErrBadVer`. Senders must migrate to v2.

## Sequencing

The `sequence` package provides one `atomic.Uint64` per shard group. When
`-proxy-seq` is enabled (default), the forwarder increments the counter for the
frame's group and stamps it into `ShardSeqNum` if the sender left it as `0`.
Per-group counters eliminate false contention between shards.

**Hot path decision:**
- Sender set `ShardSeqNum != 0` and no static overrides → zero-copy `WriteTo(raw)`.
- Otherwise → re-encode into per-worker `encodeBuf` then `WriteTo`.

## Subtree Cross-linking

`SubtreeID` (bytes 48–79) and `SubtreeHeight` (byte 7) allow downstream
subscribers to reconstruct the Merkle tree for a batch of related transactions.
The proxy can override both fields on all frames via `-static-subtree-id` and
`-static-subtree-height` when the batch context is known statically at the
proxy, rather than per-sender.

## Graceful Shutdown

`SIGINT` or `SIGTERM` closes the `done` channel. Each worker closes its ingress
socket, which unblocks the `ReadFrom` call and returns. The TCP listener's
`Accept` is also unblocked by closing the listener. `main` waits for all
goroutines with `sync.WaitGroup`.

## Package Structure

```
bitcoin-shard-proxy/
  main.go            entry point; wires config → engine → counters → forwarder → workers
  config/            runtime configuration (flags + env vars + validation)
  shard/             txid → group index → IPv6 multicast address derivation
  frame/             v2 wire format encode/decode; v1 rejection
  sequence/          per-shard atomic uint64 sequence counters
  forwarder/         decode → override → seq-stamp → egress forwarding pipeline
  worker/            per-CPU SO_REUSEPORT ingress loop; TCP ingress listener
  metrics/           OTel + Prometheus instrumentation
```
