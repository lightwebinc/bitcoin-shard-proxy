# Configuration

## Flags and environment variables

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

## Shard bits vs. group count

| `SHARD_BITS` | Groups     | Typical use case                       |
| ------------ | ---------- | -------------------------------------- |
| 2            | 4          | Ultra small; testing only              |
| 4            | 16         | Very small deployment; single switch   |
| 8            | 256        | Small lab; fits any managed switch     |
| 12           | 4,096      | Mid-scale; fits most enterprise ASICs  |
| 24           | 16,777,216 | Maximum precision; large TCAM required |

## Multicast scope

Use `site` (FF05::/16) for closed subscriber fabrics — MLD joins do not
cross router boundaries unless inter-domain multicast is explicitly
configured. Use `global` (FF0E::/16) only if subscribers span BGP domains
with PIM-SM or MSDP in the path. There are no known global multicast
deployments on the public internet; only `site` scope should be used currently.

## Assigned address space

The `-mc-base-addr` flag allows use of assigned IPv6 address space instead
of the generic zero-filled middle section. Useful when specific multicast
address ranges have been allocated by a numbers authority.

```bash
./bitcoin-shard-proxy \
  -mc-base-addr "2001:db8:1234::" \
  -scope site \
  -shard-bits 16
```

This generates addresses like `FF05:2001:db8:1234::<group_index>` instead
of the default `FF05::<group_index>`.

The base address can be any valid IPv6 address; only bytes 2–12 are used
for the middle section. The first two bytes are replaced by the multicast
prefix and scope.

## Fan-out to multiple interfaces

Every forwarded datagram is written to all listed interfaces in order,
with no copying and no extra goroutines on the hot path:

```bash
./bitcoin-shard-proxy \
  -iface       eth0,eth1 \
  -shard-bits  16        \
  -scope       site      \
  -listen-port 9000      \
  -egress-port 9001
```

## Subscriber join

Each subscriber calls `IPV6_JOIN_GROUP` (or `setsockopt MCAST_JOIN_GROUP`)
for the multicast group address(es) covering its desired shard range:

```text
FF05::<group_index>                   # Default format
FF05:2001:db8:1234::<group_index>     # With assigned address space
```

`SHARD_BITS` is a fixed, deployment-wide setting shared by all subscribers.
Doubling `SHARD_BITS` splits every existing group into two children —
subscribers join additional groups without invalidating existing ones,
so scale-up requires no redesign.
