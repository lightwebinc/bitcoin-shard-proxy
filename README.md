# bitcoin-shard-proxy

[![CI](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/jefflightweb/bitcoin-shard-proxy/actions/workflows/ci.yml)

A high-throughput UDP proxy that receives Bitcoin SV (BSV Blockchain) transaction
datagrams, derives an IPv6 multicast group address from the transaction ID,
and retransmits each datagram to the derived group for delivery to a subset
of subscribers.

Inspiration: [Multicast within Multicast: Anycast](https://singulargrit.substack.com/p/multicast-within-multicast-anycast), [Multicast as the Only Viable Architecture](https://singulargrit.substack.com/p/multicast-as-the-only-viable-architecture)

## How it works

Each incoming datagram carries a raw Bitcoin SV transaction wrapped in a compact
fixed-size header. The proxy reads the top N bits of the transaction ID
(configurable via `--shard-bits`), maps those bits to one of 2ᴺ IPv6
multicast group addresses, and forwards the original datagram verbatim to
that address. Subscribers join only the multicast groups covering the
transaction IDs they care about.

```text
sender  ──UDP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>  (iface 0)
                  (one worker / CPU)   └─────────────────►  FF05::<shard>  (iface 1)
                                                             (subset of subscribers)
```

### Shard address derivation

```text
group_index = txid[0:4] (big-endian uint32) >> (32 - SHARD_BITS)
IPv6 address = FF<scope>::<base_addr_bytes><group_index as 24-bit big-endian>
```

The multicast address consists of:

- **FF<scope>**: 16-bit multicast prefix with scope (e.g., FF05 for site-local)
- **base_addr_bytes**: 88 bits (11 bytes) from assigned IPv6 address space (configurable via `--mc-base-addr`)
- **group_index**: 24-bit shard group index derived from transaction ID

Using the top bits of the txid prefix — rather than a modulo — gives
consistent-hashing behaviour: when `SHARD_BITS` increases by 1, each
existing group splits into exactly two child groups. Existing subscriber
joins remain valid; only additional joins are needed.

### Multi-CPU design

Worker goroutines each bind an independent `SO_REUSEPORT` socket to the
same listen port. The kernel hashes each incoming datagram across the
worker sockets, distributing receive load with no userspace coordination
or channel passing on the hot path.

### Wire format

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

Single interface:

```bash
./bitcoin-shard-proxy \
  -iface       eth0 \
  -shard-bits  16   \
  -scope       site \
  -listen-port 9000 \
  -egress-port 9001
```

Fan-out to multiple interfaces simultaneously:

```bash
./bitcoin-shard-proxy \
  -iface       eth0,eth1 \
  -shard-bits  16        \
  -scope       site      \
  -listen-port 9000      \
  -egress-port 9001
```

Every forwarded datagram is written to all listed interfaces in order,
with no copying and no extra goroutines on the hot path.

All flags accept environment variable equivalents (see Configuration below).

## Configuration

| Flag             | Env var         | Default  | Description                                             |
| ---------------- | --------------- | -------- | ------------------------------------------------------- |
| `-listen`        | `LISTEN_ADDR`   | `[::]`   | Ingress bind address                                    |
| `-listen-port`   | `LISTEN_PORT`   | `9000`   | UDP port for incoming BSV transaction frames            |
| `-iface`         | `MULTICAST_IF`  | `eth0`   | NIC names for multicast egress, comma-separated         |
| `-egress-port`   | `EGRESS_PORT`   | `9001`   | Destination UDP port on multicast group addresses       |
| `-shard-bits`    | `SHARD_BITS`    | `2`      | Bit width of the shard key (1-24)                       |
| `-scope`         | `MC_SCOPE`      | `site`   | Multicast scope: `link` / `site` / `org` / `global`     |
| `-mc-base-addr`  | `MC_BASE_ADDR`  | `""`     | Base IPv6 address for assigned address space            |
| `-workers`       | `NUM_WORKERS`   | `NumCPU` | Worker goroutine count (0 = runtime.NumCPU)             |
| `-debug`         | `DEBUG`         | `false`  | Per-packet debug logging + multicast loopback           |
| `-metrics-addr`  | `METRICS_ADDR`  | `:9100`  | HTTP bind address for `/metrics`, `/healthz`, `/readyz` |
| `-instance`      | `INSTANCE_ID`   | hostname | OTel `service.instance.id` for federation               |
| `-otlp-endpoint` | `OTLP_ENDPOINT` | `""`     | OTLP gRPC push endpoint (empty = disabled)              |
| `-otlp-interval` | `OTLP_INTERVAL` | `30s`    | OTLP push interval                                      |

### Shard bits vs. group count

| `SHARD_BITS` | Groups     | Typical use case                       |
| ------------ | ---------- | -------------------------------------- |
| 2            | 4          | Ultra small; testing only              |
| 4            | 16         | Very small deployment; single switch   |
| 8            | 256        | Small lab; fits any managed switch     |
| 12           | 4,096      | Mid-scale; fits most enterprise ASICs  |
| 24           | 16,777,216 | Maximum precision; large TCAM required |

### Multicast scope

Use `site` (FF05::/16) for closed subscriber fabrics — MLD joins do not
cross router boundaries unless inter-domain multicast is explicitly
configured. Use `global` (FF0E::/16) only if subscribers span BGP domains
with PIM-SM or MSDP in the path. There are no known global multicast
deployments on the public internet, and thus only `site` scope should be used, currently.

### Assigned address space

The `-mc-base-addr` flag allows you to use assigned IPv6 address space
instead of the generic zero-filled middle section. This is useful when
you have been allocated specific multicast address ranges by your
numbers authority.

**Example with assigned space:**

```bash
./bitcoin-shard-proxy \
  -mc-base-addr "2001:db8:1234::" \
  -scope site \
  -shard-bits 16
```

This generates addresses like `FF05:2001:db8:1234::<group_index>` instead
of the default `FF05::<group_index>`.

The base address can be any valid IPv6 address; only bytes 2-12 are used
for the middle section of the multicast address. The first two bytes are
replaced by the multicast prefix and scope.

## Graceful shutdown

The proxy catches `SIGINT` (Ctrl-C) and `SIGTERM` (sent by systemd,
container orchestrators, etc.). On receipt it logs the signal name and
number, closes the internal done channel, and waits for all workers to
finish draining in-flight datagrams before exiting.

```text
time=... level=INFO msg="received signal, shutting down" signal=terminated signal_number=15
time=... level=INFO msg="all workers stopped; exiting cleanly"
```

## Subscriber join

Each subscriber calls `IPV6_JOIN_GROUP` (or `setsockopt MCAST_JOIN_GROUP`)
for the multicast group address(es) covering its desired shard range:

```text
FF05::<group_index>                   # Default format
FF05:2001:db8:1234::<group_index>     # With assigned address space
```

`SHARD_BITS` is a fixed, deployment-wide setting shared by all subscribers.
Doubling `SHARD_BITS` splits every existing group into two children — subscribers
join additional groups without invalidating existing ones, so scale-up requires
no redesign.

**`SHARD_BITS=2`** (4 groups — functional testing, join all groups):

```bash
./recv-test-frames -iface lo0 -port 9001 -groups "ff02::0,ff02::1,ff02::2,ff02::3"
./send-test-frames -addr "[::1]:9000" -shard-bits 2 -spread
```

**`SHARD_BITS=4`** (16 groups — small deployment, join a quarter for 25% coverage):

```bash
./recv-test-frames -iface lo0 -port 9001 -groups "ff02::0,ff02::1,ff02::2,ff02::3"
./send-test-frames -addr "[::1]:9000" -shard-bits 4 -spread
```

**`SHARD_BITS=8`** (256 groups — small lab, join 26 groups for ~10% coverage):

```bash
./recv-test-frames -iface lo0 -port 9001 \
  -groups "ff02::0,ff02::1,ff02::2,ff02::3,ff02::4,ff02::5,ff02::6,ff02::7,ff02::8,ff02::9,ff02::a,ff02::b,ff02::c,ff02::d,ff02::e,ff02::f,ff02::10,ff02::11,ff02::12,ff02::13,ff02::14,ff02::15,ff02::16,ff02::17,ff02::18,ff02::19"
./send-test-frames -addr "[::1]:9000" -shard-bits 8 -spread
```

The `-spread` flag sends exactly one frame per group with maximally-spaced txids,
guaranteeing full coverage verification at any `SHARD_BITS` value.

## Package structure

```text
bitcoin-shard-proxy/
├── main.go                  Entry point, signal handling, worker lifecycle
├── Dockerfile               Multi-stage build (golang:1.25 builder → ubuntu:24.04 runtime)
├── cmd/
│   ├── send-test-frames/
│   │   └── main.go          Test sender: crafts transaction frames and sends to proxy
│   ├── recv-test-frames/
│   │   └── main.go          Test receiver: joins multicast groups, prints frames
│   └── perf-test/
│       └── main.go          LXD lab throughput tester: drives proxy + collects Prometheus/pcap/interface stats
├── config/
│   └── config.go            Flag + env parsing, validation
├── frame/
│   ├── frame.go             Wire format encode/decode
│   └── frame_test.go
├── shard/
│   ├── shard.go             Txid → multicast address derivation
│   └── shard_test.go
├── metrics/
│   └── metrics.go           OTel Recorder, Prometheus + OTLP exporters, health HTTP server
├── worker/
│   └── worker.go            Receive/retransmit loop
├── test/
│   ├── run-e2e.sh           End-to-end test orchestration script (OS-aware: lo0/lo)
│   ├── Dockerfile.e2e       Single-image build with proxy + test tools (for Linux CI)
│   ├── Dockerfile.tools     Test tools image (send/recv binaries only)
│   └── docker-compose.yml   Linux CI compose (single e2e service)
├── .github/workflows/
│   ├── ci.yml               CI: unit tests + E2E on every push/PR
│   └── release.yml          Release: GitHub release on v*.* tags
├── go.mod
└── README.md
```

## Testing

### Unit tests

```bash
make test
```

### LXD lab throughput test

`perf-test` drives the full stack from outside the VMs: it sends BRC-12 frames to the proxy, scrapes Prometheus metrics before and after, collects `ip -s link` deltas from all nodes, captures per-receiver pcaps with `tcpdump`, post-processes them with `tshark` to produce a per-group delivery matrix, and writes a Markdown report.

```bash
make perf-test

./perf-test \
  -proxy-addr '[fd20::2]:9000' \
  -metrics-url http://10.10.10.20:9100 \
  -shard-bits 2 -pps 10000 -duration 5m \
  -payload-min 256 -payload-max 512 \
  -lxd -receivers recv1,recv2,recv3 \
  -output report-10k.md
```

See [bitcoin-multicast-test/testing/](https://github.com/lightwebinc/bitcoin-multicast-test/tree/main/testing) for results at 10k, 25k, and 50k pps.

### End-to-end test

Builds all binaries and runs the full send→proxy→receive pipeline:

```bash
make test-e2e
```

On macOS, multicast delivery is verified end-to-end via `recv-test-frames` on `lo0`.
On Linux, forwarding is verified via the proxy's Prometheus metrics endpoint
(`bsp_packets_forwarded_total`) — cross-process IPv6 multicast loopback on `lo`
is not reliably available in containerised or VM-based Linux environments.

The test passes with exit code 0 (`=== PASS ===`) or fails with 1 (`=== FAIL ===`).
Environment variables:

| Variable       | Default | Description                                |
| -------------- | ------- | ------------------------------------------ |
| `SHARD_BITS`   | `2`     | Shard bit width for the test run           |
| `RECV_COUNT`   | `4`     | Number of frames expected (= 2^SHARD_BITS) |
| `LISTEN_PORT`  | `9000`  | Proxy ingress port                         |
| `EGRESS_PORT`  | `9001`  | Proxy egress / receiver listen port        |
| `METRICS_PORT` | `9100`  | Proxy metrics port (Linux assertion)       |

### Manual integration test (loopback)

Run each command in a separate terminal. Use `lo` instead of `lo0` on Linux.

**Terminal 1 — start the proxy in debug mode:**

```bash
./bitcoin-shard-proxy -iface lo0 -shard-bits 8 -scope link \
  -listen-port 9000 -egress-port 9001 -debug
```

To test multi-interface fan-out on Linux (loopback has only one interface,
so use the same name twice to exercise the code path):

```bash
./bitcoin-shard-proxy -iface lo,lo -shard-bits 8 -scope link \
  -listen-port 9000 -egress-port 9001 -debug
```

**Terminal 2 — join the first four shard groups:**

```bash
./recv-test-frames -iface lo0 -port 9001 \
  -groups "ff02::0,ff02::1,ff02::2,ff02::3"
```

**Terminal 3 — send 16 frames (one per group at 8 bits):**

```bash
./send-test-frames -addr "[::1]:9000" -shard-bits 8 -count 16 -interval 100
```

The sender prints the expected destination group for each frame. The receiver
prints each frame as it arrives. The `group=` values must match.

### Watch traffic with tcpdump

Capture all IPv6 multicast UDP on the loopback (byte 24 of an IPv6 packet is
the first byte of the destination address; `0xff` matches all multicast):

```bash
sudo tcpdump -i lo0 -n "ip6 and udp and (ip6[24] == 0xff)"
```

Filter for a specific shard group, e.g. `ff02::3`:

```bash
sudo tcpdump -i lo0 -n "ip6 dst ff02::3 and udp"
```

Full hex dump for manual frame inspection:

```bash
sudo tcpdump -i lo0 -n -XX "ip6 and udp and (ip6[24] == 0xff)"
```

## Key lingering design questions

- Should ingress only accept BRC-12 format transaction frames?
- Should a different type of hash algorithm be applied to the TXID prior to determining the shard group?
- What about control messages, such as Block Headers, sequencing, subtree announcements?
- What multicast group address should be used for control messages?
- What type of mechanism should be used for multicast NACK-based retransmission?
- What frame format should be used for control messages? Should the proxy differentiate?
- How will subtree-based sharding work? What frame format?
- What about FEC?

## TODO

- [x] Test coverage
- [x] Add metrics collection and reporting
- [x] Add health check endpoints
- [x] Add more comprehensive logging
- [x] Add support for multiple egress interfaces
- [x] Add Docker image and CI/CD pipeline
- [ ] Add support for subtree-based sharding
- [ ] Add support for forward error correction (FEC)
- [ ] Add support for sequence numbering
- [ ] Add support for Negative-ACK (NACK) based retransmission

## License

Apache 2.0 - See LICENSE file.
