# Design Notes

## Open questions

- Should ingress only accept BRC-12 format transaction frames?
- Should a different hash algorithm be applied to the TXID prior to determining the shard group?
- What about control messages (block headers, sequencing, subtree announcements)?
- What multicast group address should be used for control messages?
- What mechanism should be used for multicast NACK-based retransmission?
- What frame format should be used for control messages? Should the proxy differentiate?
- How will subtree-based sharding work? What frame format?
- What about FEC?

## Roadmap

- [x] Test coverage
- [x] Metrics collection and reporting (Prometheus + OTLP)
- [x] Health check endpoints (`/healthz`, `/readyz`)
- [x] Comprehensive structured logging
- [x] Multiple egress interface fan-out
- [x] Docker image and CI/CD pipeline
- [ ] Subtree-based sharding
- [ ] Forward error correction (FEC)
- [ ] Sequence numbering
- [ ] NACK-based retransmission
