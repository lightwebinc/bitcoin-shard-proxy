# Testing

## Unit tests

```bash
make test
```

## End-to-end test

Builds all binaries and runs the full send→proxy→receive pipeline:

```bash
make test-e2e
```

On macOS, multicast delivery is verified end-to-end via `recv-test-frames` on `lo0`.
On Linux, forwarding is verified via the proxy's Prometheus metrics endpoint
(`bsp_packets_forwarded_total`) — cross-process IPv6 multicast loopback on `lo`
is not reliably available in containerised or VM-based Linux environments.

The test passes with exit code 0 (`=== PASS ===`) or fails with 1 (`=== FAIL ===`).

| Variable       | Default | Description                                |
| -------------- | ------- | ------------------------------------------ |
| `SHARD_BITS`   | `2`     | Shard bit width for the test run           |
| `RECV_COUNT`   | `4`     | Number of frames expected (= 2^SHARD_BITS) |
| `UDP_LISTEN_PORT` | `9000` | Proxy UDP ingress port                    |
| `EGRESS_PORT`  | `9001`  | Proxy egress / receiver listen port        |
| `METRICS_PORT` | `9100`  | Proxy metrics port (Linux assertion)       |

## LXD lab throughput test

`perf-test` drives the full stack from outside the VMs: it sends BRC-12 frames to
the proxy, scrapes Prometheus metrics before and after, collects `ip -s link` deltas
from all nodes, captures per-receiver pcaps with `tcpdump`, post-processes them with
`tshark` to produce a per-group delivery matrix, and writes a Markdown report.

```bash
make perf-test

./perf-test \
  -proxy-addr '[fd20::2]:9000' \
  -metrics-url http://10.10.10.20:9100 \
  -shard-bits 2 -pps 10000 -duration 5m \
  -payload-min 256 -payload-max 512 \
  -lxd -receivers recv1,recv2,recv3 \
  -output report-10k.md
```

See [bitcoin-multicast-test/testing/](https://github.com/lightwebinc/bitcoin-multicast-test/tree/main/testing) for results at 10k, 25k, and 50k pps.

## Manual integration test (loopback)

Run each command in a separate terminal. Use `lo` instead of `lo0` on Linux.

**Terminal 1 — start the proxy in debug mode:**

```bash
./bitcoin-shard-proxy -iface lo0 -shard-bits 8 -scope link \
  -udp-listen-port 9000 -egress-port 9001 -debug
```

To test multi-interface fan-out on Linux (loopback has only one interface,
so use the same name twice to exercise the code path):

```bash
./bitcoin-shard-proxy -iface lo,lo -shard-bits 8 -scope link \
  -udp-listen-port 9000 -egress-port 9001 -debug
```

**Terminal 2 — join the first four shard groups:**

```bash
./recv-test-frames -iface lo0 -port 9001 \
  -groups "ff02::0,ff02::1,ff02::2,ff02::3"
```

**Terminal 3 — send frames covering the first four groups:**

```bash
# Sequential: 16 frames, txid increments by 1 each time
./send-test-frames -addr "[::1]:9000" -shard-bits 8 -count 16 -interval 100

# Spread: exactly one frame per group per cycle (guaranteed full coverage)
./send-test-frames -addr "[::1]:9000" -shard-bits 8 -count 1 -spread
```

The sender prints the expected destination group for each frame. The receiver
prints each frame as it arrives. The `group=` values must match.

## send-test-frames reference

### Flags

| Flag          | Default      | Description                                                        |
| ------------- | ------------ | ------------------------------------------------------------------ |
| `-addr`       | `[::1]:9000` | Proxy listen address                                               |
| `-count`      | `16`         | Frames to send without `-spread`; spread cycles with `-spread` (0 = infinite) |
| `-interval`   | `200`        | Milliseconds between frames (0 = send as fast as possible)        |
| `-shard-bits` | `2`          | Must match the proxy's `-shard-bits` (used for group prediction)  |
| `-spread`     | `false`      | Send one frame per group per cycle with maximally-spaced txids     |

### Modes

**Sequential mode** (default, no `-spread`):

Txid starts at 0 and increments by 1 each frame. With small `shard-bits`,
all frames quickly land in the same group. Use `-count` to control total frames.

```bash
# Send 100 frames sequentially
./send-test-frames -addr "[::1]:9000" -shard-bits 2 -count 100 -interval 10
```

**Spread mode** (`-spread`):

Sends exactly one frame per group per cycle using maximally-spaced txids.
The txid prefix for group `g` is `g << (32 - shard_bits)`, placing `g`
in the top `shard_bits` bits. This guarantees full group coverage regardless
of `shard-bits`.

`-count` sets the number of cycles (0 = infinite):

```bash
# One cycle: exactly 2^shard_bits frames, one per group
./send-test-frames -addr "[::1]:9000" -shard-bits 2 -count 1 -spread

# 10 cycles: 10 × 2^shard_bits frames
./send-test-frames -addr "[::1]:9000" -shard-bits 2 -count 10 -spread

# Continuous: run indefinitely, cycling through all groups
./send-test-frames -addr "[fd20::2]:9000" -shard-bits 2 -count 0 -interval 0 -spread
```

### Subscriber join examples by shard-bits

**`SHARD_BITS=2`** (4 groups — join all groups):

```bash
./recv-test-frames -iface lo0 -port 9001 -groups "ff02::0,ff02::1,ff02::2,ff02::3"
./send-test-frames -addr "[::1]:9000" -shard-bits 2 -count 1 -spread
```

**`SHARD_BITS=4`** (16 groups — join a quarter for 25% coverage):

```bash
./recv-test-frames -iface lo0 -port 9001 -groups "ff02::0,ff02::1,ff02::2,ff02::3"
./send-test-frames -addr "[::1]:9000" -shard-bits 4 -count 1 -spread
```

**`SHARD_BITS=8`** (256 groups — join 26 groups for ~10% coverage):

```bash
./recv-test-frames -iface lo0 -port 9001 \
  -groups "ff02::0,ff02::1,...,ff02::19"
./send-test-frames -addr "[::1]:9000" -shard-bits 8 -count 1 -spread
```

## Capturing traffic with tcpdump

Capture all IPv6 multicast UDP on the loopback:

```bash
sudo tcpdump -i lo0 -n "ip6 and udp and (ip6[24] == 0xff)"
```

Filter for a specific shard group, e.g. `ff02::3`:

```bash
sudo tcpdump -i lo0 -n "ip6 dst ff02::3 and udp"
```

Full hex dump for manual frame inspection:

```bash
sudo tcpdump -i lo0 -n -XX "ip6 and udp and (ip6[24] == 0xff)"
```
