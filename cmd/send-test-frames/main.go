// Command send-test-frames crafts and sends well-formed BSV-over-UDP frames
// to bitcoin-shard-proxy for local integration testing.
//
// Usage:
//
//	send-test-frames [-addr host:port] [-count N] [-interval ms] [-shard-bits N] [-spread]
//
// Each frame's txid prefix increments by 1, fanning traffic across shard groups.
// With -spread, exactly one frame is sent per group using maximally-spaced txids,
// guaranteeing full coverage regardless of -count. The predicted destination
// multicast group is printed for each frame so output can be compared against
// recv-test-frames.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

func main() {
	addr := flag.String("addr", "[::1]:9000", "proxy listen address (host:port)")
	count := flag.Int("count", 16, "number of frames to send (0 = infinite)")
	intervalMs := flag.Int("interval", 200, "milliseconds between frames")
	shardBits := flag.Uint("shard-bits", 2, "shard-bits the proxy is configured with (for predicted group display)")
	spread := flag.Bool("spread", false, "send exactly one frame per group with maximally-spaced txids (ignores -count)")
	flag.Parse()

	conn, err := net.Dial("udp6", *addr)
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Printf("close conn: %v", err)
		}
	}()

	e := shard.New(0xFF05, [11]byte{}, *shardBits)
	payload := []byte("test-bsv-transaction-payload")
	buf := make([]byte, frame.HeaderSize+len(payload))
	interval := time.Duration(*intervalMs) * time.Millisecond

	fmt.Printf("sending to %s  shard_bits=%d  spread=%v\n\n", *addr, *shardBits, *spread)
	fmt.Printf("%-6s  %-10s  %-6s  %s\n", "frame", "txid[0:4]", "group", "expected_dst")

	if *spread {
		// Send exactly one frame per group. The txid prefix for group g is
		// g << (32 - shardBits), placing g in the top shardBits bits.
		numGroups := int(e.NumGroups())
		step := uint32(1) << (32 - *shardBits)
		for g := 0; g < numGroups; g++ {
			f := &frame.Frame{Payload: payload}
			txidPrefix := uint32(g) * step
			binary.BigEndian.PutUint32(f.TxID[0:4], txidPrefix)
			sendFrame(conn, e, f, buf, g, interval)
		}
		return
	}

	for i := 0; *count == 0 || i < *count; i++ {
		f := &frame.Frame{Payload: payload}
		binary.BigEndian.PutUint32(f.TxID[0:4], uint32(i))
		sendFrame(conn, e, f, buf, i, interval)
	}
}

func sendFrame(conn net.Conn, e *shard.Engine, f *frame.Frame, buf []byte, seq int, interval time.Duration) {
	n, err := frame.Encode(f, buf)
	if err != nil {
		log.Fatalf("encode frame %d: %v", seq, err)
	}
	if _, err := conn.Write(buf[:n]); err != nil {
		log.Fatalf("send frame %d: %v", seq, err)
	}
	groupIdx := e.GroupIndex(&f.TxID)
	dst := e.Addr(groupIdx, 9001)
	fmt.Printf("%-6d  %08X    %-6d  %s\n",
		seq, binary.BigEndian.Uint32(f.TxID[0:4]), groupIdx, dst.IP)
	if interval > 0 {
		time.Sleep(interval)
	}
}
