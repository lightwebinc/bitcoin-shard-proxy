# BSV Shard Proxy — Wire Protocol Specification

## 1. Overview

The BSV shard proxy transports raw BSV transactions over IPv6 UDP (or TCP for
reliable delivery) using a compact binary frame format. Every frame begins with
the BSV mainnet P2P network magic so that standard firewall rules and network
monitors already configured for BSV traffic classify proxy datagrams correctly.

## 2. v2 Frame Format (current)

**Header size:** 84 bytes, zero padding, all multi-byte fields 8-byte aligned.  
**Byte order:** big-endian for all multi-byte integers.

```
Offset  Size  Align  Field            Value / notes
------  ----  -----  -----            -------------
     0     4   —     Network magic    0xE3E1F3E8  (BSV mainnet P2P magic)
     4     2   —     Protocol ver     0x02BF = 703
     6     1   —     Frame version    0x02
     7     1   —     Reserved         0x00
     8    32   8 B   Transaction ID   raw 256-bit txid (internal byte order)
    40     8   8 B   Shard seq num    uint64 BE; sender-assigned; 0 = unset
    48    32   8 B   Subtree ID       32-byte batch identifier; all-zeros = unset
    80     4   8 B   Payload length   uint32; max 10 MiB
    84     *   4 B   BSV tx payload   raw serialised transaction bytes
```

**Alignment verification:**
| Field | Offset | Offset % 8 |
|---|---|---|
| TXID | 8 | 0 ✓ |
| ShardSeqNum | 40 | 0 ✓ |
| SubtreeID | 48 | 0 ✓ |
| PayLen | 80 | 0 ✓ |

### 2.1 Fields

**Network magic (0:4)** — `0xE3E1F3E8`. The BSV mainnet P2P network magic.
Frames that do not start with this value are rejected.

**Protocol version (4:6)** — `0x02BF` (703). The BSV node protocol version
baseline that introduced the large-block policy. This field is informational;
the proxy does not validate it.

**Frame version (6)** — `0x02` for v2, `0x01` for v1 (see §3). Any other
value is rejected. Both v1 and v2 frames are forwarded verbatim.

**Reserved (7)** — Must be `0x00`. Reserved for future use; the proxy does not
validate this byte.

**Transaction ID (8:40)** — 32 bytes. The raw 256-bit txid in internal byte
order as used in the BSV P2P protocol — **not** the reversed display order
shown by block explorers. The top bits of `txid[0:4]` are used by the shard
engine to derive the multicast group index.

**Shard sequence number (40:48)** — `uint64` big-endian. A per-shard monotonic
counter assigned by the sender. `0` means unset. The proxy forwards this field
unchanged.

**Subtree ID (48:80)** — 32 bytes. An opaque batch identifier assigned by the
transaction processor. All-zero bytes mean the field is unset. The proxy
forwards this field unchanged.

**Payload length (80:84)** — `uint32` big-endian. The number of payload bytes
immediately following the header. Must not exceed 10 MiB.

**Payload (84+)** — Raw serialised BSV transaction. Same format as the BSV P2P
`tx` message payload (version LE32 + inputs + outputs + locktime LE32). No P2P
message envelope wraps it.

---

## 3. v1 BRC-12 Frame Format

v1 frames use a 44-byte header and carry no sequence number or subtree fields.
The proxy accepts them and forwards them verbatim without modification.

```
Offset  Size  Field
------  ----  -----
     0     4  Network magic    0xE3E1F3E8
     4     2  Protocol ver     0x02BF
     6     1  Frame version    0x01
     7     1  Reserved         0x00
     8    32  Transaction ID
    40     4  Payload length
    44     *  Payload
```

**TCP ingress:** the TCP reader reads 44 bytes first to detect the version, then
completes the header read if v2 (40 more bytes). No separate port is needed for
v1 and v2 — both versions share the same listener.

---

## 4. Subtree Model

A *subtree* is an ordered set of related transactions sharing a common batch
context. The `SubtreeID` field allows downstream subscribers to associate
frames with a named batch:

- **`SubtreeID`** — 32-byte opaque batch identifier; all-zero means unset.

This field is optional. The proxy passes it through unchanged.

---

## 5. Shard Derivation

The multicast group for a frame is derived from its `TxID`:

```
groupIndex = (txid[0:4] as uint32 BE) >> (32 - shardBits)
```

where `shardBits` is the configured `-shard-bits` value (default 2, range
1–24). The group index maps to an IPv6 multicast address:

```
[FFsc::groupIndex]
```

where `sc` is the two-nibble scope code (e.g. `FF05` for site-local). The
group index occupies the three lowest bytes of the address.

**Consistent-hashing property:** increasing `shardBits` by 1 splits every
existing group into exactly two child groups. Subscribers need only join
additional groups; no existing subscriptions become invalid.

---

## 6. Proxy Forward Rules

The proxy processes each incoming datagram in two steps:

1. **Decode** — parse the frame header (v1 or v2); drop with a debug log on
   bad magic, unsupported version, oversized payload, or truncated datagram.
   The TxID is extracted to derive the destination multicast group.

2. **Forward (zero-copy)** — write the original raw bytes verbatim to every
   configured egress interface via `IPV6_MULTICAST_IF`. The frame is never
   modified; no re-encoding occurs.

---

## 7. TCP Ingress

When `-tcp-listen-port` is non-zero, the proxy also accepts TCP connections for
reliable frame delivery. The TCP wire format is identical to UDP: v1 or v2
frames concatenated end-to-end with no additional envelope.

**Read sequence per frame:**
1. Read 44 bytes (minimum header, sufficient for both v1 and the start of v2).
2. Inspect `FrameVer` at byte 6.
   - **v1:** header is complete; `PayLen` is at bytes 40–43.
   - **v2:** read 40 more bytes to complete the 84-byte header;
     `PayLen` is at bytes 80–83.
3. Read exactly `PayLen` bytes (the payload).
4. Forward the reassembled raw bytes verbatim (same as UDP path).

The proxy closes the TCP connection on any protocol violation (bad magic,
unsupported version byte, `PayLen` exceeds 10 MiB, or read error).

---

## 8. Error Handling

| Condition | UDP | TCP |
|---|---|---|
| Bad magic | datagram silently dropped | connection closed |
| Unknown frame version (not v1/v2) | datagram silently dropped | connection closed |
| PayLen > 10 MiB | datagram silently dropped | connection closed |
| Truncated datagram | datagram silently dropped | read error → connection closed |
| Egress write error | logged; next interface attempted | logged; next interface attempted |

All drops are counted in the `proxy_rx_drops_total` Prometheus metric with a
`reason` label (`decode_error`, `write_error`, or `truncated`).

---

## 9. Constants Reference

| Name | Value | Notes |
|---|---|---|
| `MagicBSV` | `0xE3E1F3E8` | BSV mainnet P2P magic |
| `ProtoVer` | `0x02BF` | Protocol version 703 |
| `FrameVerV1` | `0x01` | Legacy BRC-12; accepted, forwarded verbatim |
| `FrameVerV2` | `0x02` | Current |
| `HeaderSize` | `84` | v2 header bytes |
| `MaxPayload` | `10485760` | 10 MiB |
