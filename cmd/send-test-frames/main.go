// Command send-test-frames crafts and sends well-formed BSV-over-UDP frames
// to bitcoin-shard-proxy for local integration testing.
//
// Usage:
//
//	send-test-frames [-addr host:port] [-count N] [-interval ms] [-shard-bits N]
//
// Each frame's txid prefix increments by 1, fanning traffic across all shard
// groups. The predicted destination multicast group is printed for each frame
// so output can be compared against recv-test-frames.
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
	shardBits := flag.Uint("shard-bits", 8, "shard-bits the proxy is configured with (for predicted group display)")
	flag.Parse()

	conn, err := net.Dial("udp6", *addr)
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()

	e := shard.New(0xFF05, [11]byte{}, *shardBits)
	payload := []byte("test-bsv-transaction-payload")
	buf := make([]byte, frame.HeaderSize+len(payload))
	interval := time.Duration(*intervalMs) * time.Millisecond

	fmt.Printf("sending to %s  shard_bits=%d\n\n", *addr, *shardBits)
	fmt.Printf("%-6s  %-10s  %-6s  %s\n", "frame", "txid[0:4]", "group", "expected_dst")

	for i := 0; *count == 0 || i < *count; i++ {
		f := &frame.Frame{Payload: payload}
		binary.BigEndian.PutUint32(f.TxID[0:4], uint32(i))

		n, err := frame.Encode(f, buf)
		if err != nil {
			log.Fatalf("encode frame %d: %v", i, err)
		}
		if _, err := conn.Write(buf[:n]); err != nil {
			log.Fatalf("send frame %d: %v", i, err)
		}

		groupIdx := e.GroupIndex(&f.TxID)
		dst := e.Addr(groupIdx, 9001)
		fmt.Printf("%-6d  %08X    %-6d  %s\n",
			i, binary.BigEndian.Uint32(f.TxID[0:4]), groupIdx, dst.IP)

		if interval > 0 {
			time.Sleep(interval)
		}
	}
}
