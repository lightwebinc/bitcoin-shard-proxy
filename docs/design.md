# Design Notes

## Open questions

- Should a different hash algorithm be applied to the TXID prior to determining the shard group?
- What multicast group address should be used for control messages?
- What frame format should be used for control messages? Should the proxy differentiate?
- NACK retransmission via multicast retry endpoints: wire protocol finalised (24-byte NACK datagram per BRC-TBD-retransmission); retry endpoint implemented.
- FEC: deferred — full frame atomicity makes partial repair unproductive; full re-multicast preferred.

## Roadmap

- [x] Test coverage
- [x] Metrics collection and reporting (Prometheus + OTLP)
- [x] Health check endpoints (`/healthz`, `/readyz`)
- [x] Comprehensive structured logging
- [x] Multiple egress interface fan-out
- [x] Docker image and CI/CD pipeline
- [x] Subtree sharding cross-linking fields in BRC-124 frame header
- [x] TCP ingress for reliable ingress delivery (`-tcp-listen-port`)
- [x] Configurable pre-drain period for load-balancer-safe rolling restarts (`-drain-timeout`)
- [x] PrevSeq/CurSeq hash chain stamping (XXH64, replaces CRC32c SenderID)
- [x] BRC-124 frame format: 92-byte header, PrevSeq/CurSeq (8-byte XXH64 hash chain fields)
- [x] NACK / gap-detection via multicast retry endpoints (see bitcoin-shard-listener)
- [x] Retry endpoint service (cache + re-multicast on NACK)
- [ ] FEC (forward error correction) option for lossy links
- [ ] Shard manifest protocol (publish current shard map to subscribers)
- [ ] Add support for base control group frames (subtree, headers, manifests, etc.)
