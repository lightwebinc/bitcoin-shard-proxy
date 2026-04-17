# Architecture

## Overview

bitcoin-shard-proxy receives BSV transaction frames (v1 or v2) over UDP (and
optionally TCP), derives a deterministic multicast group address from each
transaction's txid, then retransmits the original bytes verbatim to all
configured egress interfaces.

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

### v2 (current — 100 bytes, 8-byte aligned, zero padding)

```
Offset  Size  Align  Field
------  ----  -----  -----
     0     4   —     Network magic    0xE3E1F3E8
     4     2   —     Protocol ver     0x02BF
     6     1   —     Frame version    0x02
     7     1   —     Reserved         0x00
     8    32   8 B   Transaction ID   raw 256-bit txid (internal byte order)
    40     8   8 B   Shard seq num    uint64 BE; 0 = unset
    48    32   8 B   Subtree ID       32-byte batch identifier; zeros = unset
    80    16   8 B   Sender ID        original BSV sender IPv6 (net.IP.To16()); zeros = unset
    96     4   4 B   Payload length   uint32 BE
   100     *   —     BSV tx payload
```

### v1 BRC-12 (legacy — 44 bytes, accepted, forwarded verbatim)

```
Offset  Size  Align  Field            Value / notes
------  ----  -----  -----            -------------
     0     4   —     Network magic    0xE3E1F3E8
     4     2   —     Protocol ver     0x02BF = 703
     6     1   —     Frame version    0x01
     7     1   —     Reserved         0x00
     8    32   —     Transaction ID   raw 256-bit txid (internal byte order)
    40     4   —     Payload length   uint32 BE; max 10 MiB
    44     *   —     BSV tx payload   raw serialised transaction bytes
```

v1 frames carry no `ShardSeqNum`, `SubtreeID`, or `SenderID` fields beyond
byte 7. The proxy accepts them and forwards the original bytes unchanged.

## Hot Path

Every received datagram follows the same path:
1. `frame.Decode(raw)` — extract the TxID; drop on bad magic or unknown version.
2. **SenderID stamp (v2 only)** — overwrite `raw[80:96]` in-place with the
   ingress source address (`net.IP.To16()`). v1 frames are untouched.
3. `WriteTo(raw)` — write the raw bytes to every egress target.

No re-encoding, no per-worker encode buffer.

## Graceful Shutdown

Shutdown proceeds in two phases when `SIGINT` or `SIGTERM` is received:

1. **Drain** — `rec.SetDraining()` is called immediately, flipping `/readyz`
   to `503` so load balancers stop routing new connections. If `-drain-timeout`
   is non-zero, the process sleeps for that duration while workers continue
   forwarding in-flight packets.

2. **Quiesce** — The `done` channel is closed. Each UDP worker and the TCP
   listener close their ingress sockets, unblocking any pending `ReadFrom` /
   `Accept` calls. Active TCP connections are force-closed so `handleConn`
   goroutines do not hang. `main` waits for all goroutines via
   `sync.WaitGroup`, then flushes the OTLP exporter before returning.

## Package Structure

```
bitcoin-shard-proxy/
  main.go            entry point; wires config → engine → forwarder → workers
  config/            runtime configuration (flags + env vars + validation)
  shard/             txid → group index → IPv6 multicast address derivation
  frame/             v1/v2 wire format decode; encode used by tests and tooling
  forwarder/         decode → zero-copy verbatim forward pipeline
  worker/            per-CPU SO_REUSEPORT ingress loop; TCP ingress listener
  metrics/           OTel + Prometheus instrumentation
```
