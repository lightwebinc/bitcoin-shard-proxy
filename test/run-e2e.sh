#!/bin/sh
set -e

SHARD_BITS=${SHARD_BITS:-2}
RECV_COUNT=${RECV_COUNT:-4}
LISTEN_PORT=${LISTEN_PORT:-9000}
EGRESS_PORT=${EGRESS_PORT:-9001}

# Detect loopback interface name (lo0 on macOS, lo on Linux)
if [ "$(uname)" = "Darwin" ]; then
    LOOPBACK="lo0"
else
    LOOPBACK="lo"
    # Ensure loopback has the MULTICAST flag and a multicast route on Linux
    ip link set lo multicast on 2>/dev/null || true
    ip -6 route add ff00::/8 dev lo table local 2>/dev/null || true
fi

# Compute multicast group list: ff02::0 through ff02::<N-1>
num_groups=$(( 1 << SHARD_BITS ))
groups=""
i=0
while [ $i -lt $num_groups ]; do
    if [ -z "$groups" ]; then
        groups="ff02::$i"
    else
        groups="$groups,ff02::$i"
    fi
    i=$(( i + 1 ))
done

echo "=== E2E test: shard_bits=$SHARD_BITS iface=$LOOPBACK groups=$groups ==="

# Start receiver in background
recv-test-frames \
    -iface "$LOOPBACK" \
    -port "$EGRESS_PORT" \
    -groups "$groups" \
    -count "$RECV_COUNT" &
RECV_PID=$!

# Start proxy in background
bitcoin-shard-proxy \
    -iface "$LOOPBACK" \
    -scope link \
    -shard-bits "$SHARD_BITS" \
    -listen-port "$LISTEN_PORT" \
    -egress-port "$EGRESS_PORT" \
    -debug &
PROXY_PID=$!

# Give proxy and receiver time to initialise
sleep 1

# Send frames
send-test-frames \
    -addr "[::1]:$LISTEN_PORT" \
    -shard-bits "$SHARD_BITS" \
    -spread \
    -interval 100

# Wait for receiver to finish (exits once RECV_COUNT frames received)
wait $RECV_PID
RECV_EXIT=$?

kill $PROXY_PID 2>/dev/null || true
wait $PROXY_PID 2>/dev/null || true

if [ $RECV_EXIT -eq 0 ]; then
    echo "=== PASS: received $RECV_COUNT frames ==="
    exit 0
else
    echo "=== FAIL: receiver exited with code $RECV_EXIT ==="
    exit 1
fi
