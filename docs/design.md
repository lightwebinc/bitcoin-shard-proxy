# Design Notes

## Open questions

- Should a different hash algorithm be applied to the TXID prior to determining the shard group?
- What multicast group address should be used for control messages?
- CDN design for multicast NACK-based retransmission.
- What frame format should be used for control messages? Should the proxy differentiate?
- What about FEC?

## Roadmap

- [x] Test coverage
- [x] Metrics collection and reporting (Prometheus + OTLP)
- [x] Health check endpoints (`/healthz`, `/readyz`)
- [x] Comprehensive structured logging
- [x] Multiple egress interface fan-out
- [x] Docker image and CI/CD pipeline
- [x] Subtree sharding cross-linking fields in v2 frame header
- [x] TCP ingress for reliable ingress delivery (`-tcp-listen-port`)
- [x] Configurable pre-drain period for load-balancer-safe rolling restarts (`-drain-timeout`)
- [ ] Sequence number generation (either external, or internal, or both)
- [ ] NACK / gap-detection protocol over CDN
- [ ] FEC (forward error correction) option for lossy links
- [ ] Shard manifest protocol (publish current shard map to subscribers)
- [ ] Add support for base control group frames (subtree, headers, manifests, etc.)
