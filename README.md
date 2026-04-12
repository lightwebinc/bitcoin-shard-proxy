# bitcoin-shard-proxy

[![CI](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml)

A high-throughput proxy that receives Bitcoin SV (BSV Blockchain) v2 transaction
frames over UDP (or TCP for reliable delivery), derives an IPv6 multicast group
address from the transaction ID, optionally stamps a proxy-assigned sequence
number and static subtree fields, and retransmits to a subset of subscribers.

Inspiration: [Multicast within Multicast: Anycast](https://singulargrit.substack.com/p/multicast-within-multicast-anycast), [Multicast as the Only Viable Architecture](https://singulargrit.substack.com/p/multicast-as-the-only-viable-architecture)

```text
sender  ──UDP/TCP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>  (iface 0)
                      (forwarder pipeline) └─────────────────►  FF05::<shard>  (iface 1)
                                                                 (subset of subscribers)
```

## Documentation

- [Protocol](docs/protocol.md) — v2 wire format, shard derivation, proxy transform rules, TCP ingress, error handling
- [Architecture](docs/architecture.md) — system overview, multi-CPU design, sequencing, subtree cross-linking, package structure
- [Configuration](docs/configuration.md) — all flags, environment variables, ingress modes, sequencing, subtree overrides
- [Testing](docs/testing.md) — unit tests, e2e test, LXD perf test, manual loopback test, `send-test-frames` reference
- [Design Notes](docs/design.md) — resolved questions, open questions, roadmap

## Requirements

- Go 1.25 or later
- Linux kernel 3.9+, FreeBSD 12.3+ (for `SO_REUSEPORT`), MacOS
- IPv6 enabled on the egress interface(s)
- Multicast routing / MLD snooping configured for your subscriber fabric
- Bitcoin SV ingress transaction packets in BRC-12 format (V1), or extended sequence subtree format (V2).

## Build

```bash
make            # builds bitcoin-shard-proxy, send-test-frames, recv-test-frames
make test       # runs unit tests
make test-e2e   # end-to-end test (builds all binaries, runs test/run-e2e.sh)
make clean      # removes built binaries
```

## Run

```bash
./bitcoin-shard-proxy \
  -iface            eth0 \
  -shard-bits       16   \
  -scope            site \
  -udp-listen-port  9000 \
  -egress-port      9001
```

With TCP ingress and proxy sequence stamping (defaults on):

```bash
./bitcoin-shard-proxy \
  -iface            eth0 \
  -udp-listen-port  9000 \
  -tcp-listen-port  9100 \
  -proxy-seq        true
```

See [docs/configuration.md](docs/configuration.md) for all flags and environment variable equivalents.

## License

Apache 2.0 - See LICENSE file.
