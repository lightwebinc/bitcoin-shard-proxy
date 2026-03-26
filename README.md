# bitcoin-shard-proxy

A high-throughput UDP proxy that receives BSV (Bitcoin SV) transaction
datagrams, derives an IPv6 multicast group address from the transaction ID,
and retransmits each datagram to the derived group for delivery to a subset
of subscribers.

## How it works

Each incoming datagram carries a raw BSV transaction wrapped in a compact
fixed-size header. The proxy reads the top N bits of the transaction ID
(configurable via `--shard-bits`), maps those bits to one of 2ᴺ IPv6
multicast group addresses, and forwards the original datagram verbatim to
that address. Subscribers join only the multicast groups covering the
transaction IDs they care about.

```text
sender  ──UDP──►  bitcoin-shard-proxy  ──UDP multicast──►  FF05::<shard>
                  (one worker / CPU)                        (subset of subscribers)
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

| Offset | Size | Field          | Value                              |
|--------|------|----------------|------------------------------------|
| 0      | 4 B  | Network magic  | `0xE3E1F3E8` (BSV mainnet P2P)     |
| 4      | 2 B  | Protocol ver   | `0x02BF` (703, BSV node baseline)  |
| 6      | 1 B  | Frame version  | `0x01`                             |
| 7      | 1 B  | Reserved       | `0x00`                             |
| 8      | 32 B | Transaction ID | Raw 256-bit txid (internal order)  |
| 40     | 4 B  | Payload length | uint32, max 10 MiB                 |
| 44     | var  | BSV tx payload | Raw serialised BSV transaction     |

The txid at offset 8 is in internal byte order (as used in the BSV P2P
protocol), not the reversed display order shown by block explorers.

## Requirements

- Go 1.26.1 or later
- Linux kernel 3.9+ (for `SO_REUSEPORT`)
- IPv6 enabled on the egress interface
- Multicast routing / MLD snooping configured for your subscriber fabric

## Build

```bash
go build -o bitcoin-shard-proxy ./cmd/bitcoin-shard-proxy/
```

## Run

```bash
./bitcoin-shard-proxy \
  -iface     eth0 \
  -shard-bits 16  \
  -scope      site \
  -listen-port 9000 \
  -egress-port 9001
```

All flags accept environment variable equivalents (see Configuration below).

## Configuration

| Flag           | Env var        | Default      | Description                                      |
|----------------|----------------|--------------|--------------------------------------------------|
| `-listen`      | `LISTEN_ADDR`  | `[::]`       | Ingress bind address                             |
| `-listen-port` | `LISTEN_PORT`  | `9000`       | UDP port for incoming BSV transaction frames     |
| `-iface`       | `MULTICAST_IF` | `eth0`       | NIC for multicast egress                         |
| `-egress-port` | `EGRESS_PORT`  | `9001`       | Destination UDP port on multicast group addresses|
| `-shard-bits`  | `SHARD_BITS`   | `16`         | Bit width of the shard key (1–24)                |
| `-scope`       | `MC_SCOPE`     | `site`       | Multicast scope: `link` / `site` / `org` / `global` |
| `-mc-base-addr`| `MC_BASE_ADDR` | `""`         | Base IPv6 address for assigned address space     |
| `-workers`     | `NUM_WORKERS`  | `NumCPU`     | Worker goroutine count (0 = runtime.NumCPU)      |
| `-debug`       | —              | `false`      | Per-packet debug logging + multicast loopback    |

### Shard bits vs. group count

| `SHARD_BITS` | Groups     | Typical use case                        |
|-------------|------------|-----------------------------------------|
| 8           | 256        | Small lab; fits any managed switch      |
| 12          | 4,096      | Mid-scale; fits most enterprise ASICs   |
| 16          | 65,536     | Standard deployment                     |
| 24          | 16,777,216 | Maximum precision; large TCAM required  |

### Multicast scope

Use `site` (FF05::/16) for closed subscriber fabrics — MLD joins do not
cross router boundaries unless inter-domain multicast is explicitly
configured. Use `global` (FF0E::/16) only if subscribers span BGP domains
with PIM-SM or MSDP in the path.

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
FF05::<group_index>                    # Default format
FF05:2001:db8:1234::<group_index>     # With assigned address space
```

With `SHARD_BITS=16` a subscriber covering 1% of transaction volume joins
approximately 655 groups — well within any modern MLD table.

## Package structure

```text
bitcoin-shard-proxy/
├── cmd/bitcoin-shard-proxy/
│   └── main.go          Entry point, signal handling, worker lifecycle
├── internal/
│   ├── config/
│   │   └── config.go    Flag + env parsing, validation
│   ├── frame/
│   │   └── frame.go     BSV-over-UDP wire format encode/decode
│   ├── shard/
│   │   └── shard.go     txid → multicast address derivation
│   └── worker/
│       └── worker.go    Per-CPU SO_REUSEPORT receive/retransmit loop
├── go.mod
└── README.md
```

## License

See LICENSE file.
