# BSV Shard Proxy — Wire Protocol

The canonical wire format specification lives in the shared protocol primitives
module:

**[bitcoin-shard-common/docs/protocol.md](https://github.com/lightwebinc/bitcoin-shard-common/blob/main/docs/protocol.md)**

It covers the BRC-124 and v1 frame layouts, field definitions, shard derivation
algorithm, and constants reference.

---

## Proxy-specific behaviour

The following proxy-side transformations are applied before forwarding and are
documented in [docs/architecture.md](architecture.md) and
[docs/configuration.md](configuration.md):

- **PrevSeq/CurSeq stamping** — for BRC-124 frames, if `raw[48:56]` (CurSeq)
  is already non-zero the sender has pre-stamped the frame and it is forwarded
  verbatim. If CurSeq is zero the proxy stamps `raw[40:48]` (PrevSeq) and
  `raw[48:56]` (CurSeq) in-place with XXH64 hash chain values per
  `(senderIPv6, groupIdx)`. v1 frames are always forwarded verbatim.
- **TCP ingress** — frames may arrive over TCP as well as UDP; the proxy reads
  the v1/BRC-124 header to determine frame boundaries and dispatches to the same
  forwarding pipeline.
- **BRC-127 SubtreeAnnounce forwarding** — when a TCP client sends a frame with
  `MsgType 0x30` (SubtreeAnnounce), the proxy forwards the 64-byte datagram to
  the `CtrlGroupSubtreeAnnounce` (`0xFFFFFC`) control-plane multicast group
  without sequence stamping.
- **Error handling** — bad magic, unknown version, oversized payload, and
  truncated datagrams are dropped and counted in `bsp_packets_dropped_total`.
