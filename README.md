# bitcoin-shard-proxy

[![CI](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml)

A high-throughput UDP proxy that receives Bitcoin SV (BSV Blockchain) transaction
datagrams, derives an IPv6 multicast group address from the transaction ID,
and retransmits each datagram to the derived group for delivery to a subset
of subscribers.

Inspiration: [Multicast within Multicast: Anycast](https://singulargrit.substack.com/p/multicast-within-multicast-anycast), [Multicast as the Only Viable Architecture](https://singulargrit.substack.com/p/multicast-as-the-only-viable-architecture)

```text
sender  ──UDP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>  (iface 0)
                  (one worker / CPU)   └─────────────────►  FF05::<shard>  (iface 1)
                                                             (subset of subscribers)
```

## Documentation

- [Architecture](docs/architecture.md) — how it works, shard derivation, wire format, multi-CPU design, package structure
- [Configuration](docs/configuration.md) — all flags, environment variables, shard bits table, multicast scope, subscriber join
- [Testing](docs/testing.md) — unit tests, e2e test, LXD perf test, manual loopback test, `send-test-frames` reference
- [Design Notes](docs/design.md) — open questions and roadmap

## Requirements

- Go 1.25 or later
- Linux kernel 3.9+, FreeBSD 12.3+ (for `SO_REUSEPORT`)
- IPv6 enabled on the egress interface(s)
- Multicast routing / MLD snooping configured for your subscriber fabric
- Bitcoin SV ingress transaction packets in BRC-12 format

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
  -iface       eth0 \
  -shard-bits  16   \
  -scope       site \
  -listen-port 9000 \
  -egress-port 9001
```

See [docs/configuration.md](docs/configuration.md) for all flags and environment variable equivalents.

## License

Apache 2.0 - See LICENSE file.
