# Architecture

## How it works

Each incoming datagram carries a raw Bitcoin SV transaction wrapped in a compact
fixed-size header. The proxy reads the top N bits of the transaction ID
(configurable via `-shard-bits`), maps those bits to one of 2ᴺ IPv6
multicast group addresses, and forwards the original datagram verbatim to
that address. Subscribers join only the multicast groups covering the
transaction IDs they care about.

```text
sender  ──UDP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>  (iface 0)
                  (one worker / CPU)   └─────────────────►  FF05::<shard>  (iface 1)
                                                             (subset of subscribers)
```

## Shard address derivation

```text
group_index = txid[0:4] (big-endian uint32) >> (32 - SHARD_BITS)
IPv6 address = FF<scope>::<base_addr_bytes><group_index as 24-bit big-endian>
```

The top `SHARD_BITS` bits of the first four bytes of the txid are extracted via a right-shift, producing an integer in the range `[0, 2ᴺ)` that indexes one of the 2ᴺ multicast groups. That index is then packed into the low 24 bits of the IPv6 multicast address, with the scope byte and an optional operator-assigned base address filling the remaining fields.

The multicast address consists of:

- **FF\<scope\>**: 16-bit multicast prefix with scope (e.g., FF05 for site-local)
- **base_addr_bytes**: 88 bits (11 bytes) from assigned IPv6 address space (configurable via `-mc-base-addr`)
- **group_index**: 24-bit shard group index derived from transaction ID

Using the top bits of the txid prefix — rather than a modulo — gives
consistent-hashing behaviour: when `SHARD_BITS` increases by 1, each
existing group splits into exactly two child groups. Existing subscriber
joins remain valid; only additional joins are needed.

## Multi-CPU design

Worker goroutines each bind an independent `SO_REUSEPORT` socket to the
same listen port. The kernel hashes each incoming datagram across the
worker sockets, distributing receive load with no userspace coordination
or channel passing on the hot path.

## Wire format

All multi-byte integers big-endian:

| Offset | Size | Field          | Value                             |
| ------ | ---- | -------------- | --------------------------------- |
| 0      | 4 B  | Network magic  | `0xE3E1F3E8` (BSV mainnet P2P)    |
| 4      | 2 B  | Protocol ver   | `0x02BF` (703, BSV node baseline) |
| 6      | 1 B  | Frame version  | `0x01`                            |
| 7      | 1 B  | Reserved       | `0x00`                            |
| 8      | 32 B | Transaction ID | Raw 256-bit txid (internal order) |
| 40     | 4 B  | Payload length | uint32, max 10 MiB                |
| 44     | var  | Tx payload     | Raw serialised BRC-12 format txn  |

The txid at offset 8 is in internal byte order (as used in the BSV P2P
protocol), not the reversed display order shown by block explorers.

## Graceful shutdown

The proxy catches `SIGINT` (Ctrl-C) and `SIGTERM` (sent by systemd,
container orchestrators, etc.). On receipt it logs the signal name and
number, closes the internal done channel, and waits for all workers to
finish draining in-flight datagrams before exiting.

```text
time=... level=INFO msg="received signal, shutting down" signal=terminated signal_number=15
time=... level=INFO msg="all workers stopped; exiting cleanly"
```

## Package structure

```text
bitcoin-shard-proxy/
├── main.go                  Entry point, signal handling, worker lifecycle
├── Dockerfile               Multi-stage build (golang:1.25 builder → ubuntu:24.04 runtime)
├── cmd/
│   ├── send-test-frames/    Test sender: crafts transaction frames and sends to proxy
│   ├── recv-test-frames/    Test receiver: joins multicast groups, prints frames
│   └── perf-test/           LXD lab throughput tester: drives proxy + collects Prometheus/pcap/interface stats
├── config/                  Flag + env parsing, validation
├── frame/                   Wire format encode/decode
├── shard/                   Txid → multicast address derivation
├── metrics/                 OTel Recorder, Prometheus + OTLP exporters, health HTTP server
├── worker/                  Receive/retransmit loop
└── test/                    End-to-end test scripts and Dockerfiles
```
