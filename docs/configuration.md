# Configuration Reference

All parameters are accepted from CLI flags first; environment variables serve
as fallbacks; hard-coded defaults apply when neither is present.

## Flags and Environment Variables

| Flag | Env var | Default | Description |
|---|---|---|---|
| `-listen` | `LISTEN_ADDR` | `[::]` | Ingress bind address (without port) |
| `-udp-listen-port` | `UDP_LISTEN_PORT` | `9000` | UDP listen port for incoming BSV v2 transaction frames |
| `-tcp-listen-port` | `TCP_LISTEN_PORT` | `0` | TCP ingress port for reliable delivery (0 = disabled) |
| `-iface` | `MULTICAST_IF` | `eth0` | Comma-separated NIC names for multicast egress |
| `-egress-port` | `EGRESS_PORT` | `9001` | Destination UDP port for multicast groups |
| `-shard-bits` | `SHARD_BITS` | `2` | Key bit width (1–24) |
| `-scope` | `MC_SCOPE` | `site` | Multicast scope: `link` \| `site` \| `org` \| `global` |
| `-mc-base-addr` | `MC_BASE_ADDR` | `""` | Base IPv6 address for assigned multicast address space (bytes 2–12) |
| `-workers` | `NUM_WORKERS` | `runtime.NumCPU()` | Worker goroutine count (0 = NumCPU) |
| `-proxy-seq` | `PROXY_SEQ` | `true` | Assign `ShardSeqNum` fallback when sender leaves it 0 |
| `-static-subtree-id` | `STATIC_SUBTREE_ID` | `""` | Hex-encoded 32-byte `SubtreeID` override (empty = passthrough) |
| `-static-subtree-height` | `STATIC_SUBTREE_HEIGHT` | `""` | `SubtreeHeight` override: `""` = passthrough; `"0"`–`"255"` = explicit value |
| `-debug` | `DEBUG` | `false` | Enable per-packet debug logging and multicast loopback |
| `-metrics-addr` | `METRICS_ADDR` | `:9100` | HTTP bind address for `/metrics`, `/healthz`, `/readyz` |
| `-instance` | `INSTANCE_ID` | hostname | OTel `service.instance.id` for federation |
| `-otlp-endpoint` | `OTLP_ENDPOINT` | `""` | OTLP gRPC endpoint (empty = disabled) |
| `-otlp-interval` | `OTLP_INTERVAL` | `30s` | OTLP push interval |

---

## Ingress Modes

The proxy supports two ingress transports. Both feed the same forwarding
pipeline; you may run both simultaneously.

### UDP ingress (default)

UDP ingress uses `SO_REUSEPORT` to distribute incoming datagrams across all
worker goroutines with no userspace coordination. This is the high-throughput
path.

```
-udp-listen-port 9000   # (default)
```

### TCP ingress (optional)

TCP ingress provides reliable, ordered delivery for senders that require it
(e.g. over lossy links). Each accepted connection carries a stream of v2 frames
concatenated end-to-end. The proxy reads the 84-byte header, extracts
`PayLen`, then reads the payload.

TCP ingress is disabled by default. Enable it with:

```
-tcp-listen-port 9100
```

Both transports can run at the same time:

```
bitcoin-shard-proxy \
  -iface eth0 \
  -udp-listen-port 9000 \
  -tcp-listen-port 9100
```

---

## Shard Bits

`-shard-bits N` configures the number of txid prefix bits used to derive the
multicast group index. The total number of groups is 2^N.

| Bits | Groups | Typical use |
|---|---|---|
| 1 | 2 | Minimal / testing |
| 2 | 4 | Default |
| 8 | 256 | Medium deployments |
| 16 | 65 536 | Large deployments |
| 24 | 16 777 216 | Maximum |

Increasing bits by 1 splits every existing group into two child groups
(consistent hashing). Subscribers need only join additional groups.

---

## Sequencing

Each multicast group maintains an independent monotonic `ShardSeqNum` counter.
Subscribers can use this counter to detect gaps (dropped datagrams) and to
order out-of-order delivery.

### `-proxy-seq` (default `true`)

When enabled, the proxy stamps `ShardSeqNum` on any frame where the sender
left it as `0`. The counter starts at 0 and increments atomically per group.

```
-proxy-seq=false   # disable; forward ShardSeqNum verbatim (always 0 if sender
                   # does not set it)
```

**Fast path:** if the sender already sets `ShardSeqNum != 0` and no static
overrides are configured, the proxy forwards the original bytes verbatim
(zero-copy `WriteTo`). Re-encoding only occurs when stamping is needed.

---

## Static Subtree Overrides

These flags override the `SubtreeID` and `SubtreeHeight` fields on **every**
forwarded frame. They are useful when a single proxy deployment serves a fixed
batch context.

### `-static-subtree-id`

A 64-character hexadecimal string representing 32 bytes. When set, the proxy
replaces `SubtreeID` in every frame before forwarding.

```bash
# Override SubtreeID to all 0xAB bytes (32 bytes = 64 hex chars):
-static-subtree-id abababababababababababababababababababababababababababababababababab
```

Empty string (default) means passthrough — the sender's `SubtreeID` is
preserved.

### `-static-subtree-height`

A decimal string in the range `"0"`–`"255"`. When set, the proxy replaces
`SubtreeHeight` in every frame before forwarding. An empty string (default)
means passthrough.

**Note:** `"0"` is a valid override value (it explicitly sets `SubtreeHeight`
to 0, meaning "unset"), distinct from the empty string which disables the
override entirely.

```bash
# Force SubtreeHeight = 20 on all frames:
-static-subtree-height 20

# Force SubtreeHeight = 0 (clear the field) on all frames:
-static-subtree-height 0

# Passthrough (default):
# -static-subtree-height ""
```

---

## Multicast Scope

| Value | Prefix | Reach |
|---|---|---|
| `link` | `FF02` | Same L2 segment only |
| `site` | `FF05` | Site-local (default; crosses routers within a site) |
| `org` | `FF08` | Organisation-wide |
| `global` | `FF0E` | Internet-routable |

---

## Metrics Endpoints

The metrics HTTP server (default `:9100`) exposes:

- **`/metrics`** — Prometheus text format
- **`/healthz`** — Always `200 OK` if the process is running
- **`/readyz`** — `200` when all workers are ready; `503` during drain

---

## Example Invocations

### Minimal (single NIC, defaults)

```bash
bitcoin-shard-proxy -iface eth0
```

### Multi-NIC, custom shard bits, OTLP

```bash
bitcoin-shard-proxy \
  -iface eth0,eth1 \
  -shard-bits 8 \
  -udp-listen-port 9000 \
  -egress-port 9001 \
  -otlp-endpoint collector:4317
```

### With TCP ingress and subtree overrides

```bash
bitcoin-shard-proxy \
  -iface eth0 \
  -udp-listen-port 9000 \
  -tcp-listen-port 9100 \
  -static-subtree-id $(python3 -c "print('ab'*32)") \
  -static-subtree-height 20
```

### Disable proxy sequence stamping

```bash
bitcoin-shard-proxy \
  -iface eth0 \
  -proxy-seq=false
```

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
  -udp-listen-port 9000  \
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
