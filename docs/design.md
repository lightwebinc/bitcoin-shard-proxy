# Design Notes

## Resolved Questions

1. **Frame format for subtree cross-linking** — resolved in v2 (84-byte
   header). `SubtreeID` at bytes 48–79 (32 bytes, 8-byte aligned) and
   `SubtreeHeight` at byte 7 (former Reserved slot). No separate port or
   flag bit; fields are always present and use all-zero / zero as "unset".

2. **How does the proxy differentiate which frames carry subtree
   information?** — it does not need to. Fields are always present in v2.
   The proxy forwards them verbatim unless `-static-subtree-id` /
   `-static-subtree-height` overrides are configured.

3. **`StaticSubtreeHeight = 0` ambiguity** — resolved by using `*uint8` in
   `config.Config`: `nil` means passthrough; `*0` is an explicit override
   to zero. The CLI flag uses empty string for passthrough and `"0"`–`"255"`
   for an explicit value.

## Open questions

- Should ingress only accept BRC-12 format transaction frames?
- Should a different hash algorithm be applied to the TXID prior to determining the shard group?
- What multicast group address should be used for control messages?
- What mechanism should be used for multicast NACK-based retransmission?
- What frame format should be used for control messages? Should the proxy differentiate?
- What about FEC?

## Roadmap

- [x] Test coverage
- [x] Metrics collection and reporting (Prometheus + OTLP)
- [x] Health check endpoints (`/healthz`, `/readyz`)
- [x] Comprehensive structured logging
- [x] Multiple egress interface fan-out
- [x] Docker image and CI/CD pipeline
- [x] Sequence numbering per shard group (`-proxy-seq`, `sequence` package)
- [x] Subtree cross-linking fields in v2 frame header
- [x] TCP ingress for reliable delivery (`-tcp-listen-port`)
- [x] Static subtree overrides (`-static-subtree-id`, `-static-subtree-height`)
- [ ] NACK / gap-detection protocol over TCP back-channel
- [ ] FEC (forward error correction) option for lossy links
- [ ] Shard manifest protocol (publish current shard map to subscribers)
- [ ] v1 migration tooling (convert old senders to v2 frame format)
- [ ] Subtree-based sharding
