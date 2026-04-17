# BSV Shard Proxy — Wire Protocol

The canonical wire format specification lives in the shared protocol primitives
module:

**[bitcoin-shard-common/docs/protocol.md](https://github.com/lightwebinc/bitcoin-shard-common/blob/main/docs/protocol.md)**

It covers the v2 and v1 frame layouts, field definitions, shard derivation
algorithm, and constants reference.

---

## Proxy-specific behaviour

The following proxy-side transformations are applied before forwarding and are
documented in [docs/architecture.md](architecture.md) and
[docs/configuration.md](configuration.md):

- **SenderID stamping** — for v2 frames, `raw[80:96]` is overwritten in-place
  with the ingress source address (`net.IP.To16()`) before the datagram is
  written to egress targets. v1 frames are forwarded verbatim.
- **TCP ingress** — frames may arrive over TCP as well as UDP; the proxy reads
  the v1/v2 header to determine frame boundaries and dispatches to the same
  forwarding pipeline.
- **Error handling** — bad magic, unknown version, oversized payload, and
  truncated datagrams are dropped and counted in `bsp_packets_dropped_total`.
