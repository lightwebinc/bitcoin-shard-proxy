FROM golang:1.25 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /bitcoin-shard-proxy .

FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bitcoin-shard-proxy /usr/local/bin/bitcoin-shard-proxy

ENV LISTEN_ADDR="[::]" \
    UDP_LISTEN_PORT="9000" \
    TCP_LISTEN_PORT="0" \
    MULTICAST_IF="eth0" \
    EGRESS_PORT="9001" \
    SHARD_BITS="2" \
    MC_SCOPE="site" \
    MC_GROUP_ID="0x000B" \
    NUM_WORKERS="" \
    FRAG_MTU="0" \
    DRAIN_TIMEOUT="0s" \
    METRICS_ADDR=":9100" \
    INSTANCE_ID="" \
    OTLP_ENDPOINT="" \
    OTLP_INTERVAL="30s"

ENTRYPOINT ["bitcoin-shard-proxy"]
