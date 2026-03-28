// Command recv-test-frames joins one or more IPv6 multicast groups and prints
// every BSV-over-UDP frame it receives. Use alongside send-test-frames to
// verify that bitcoin-shard-proxy is forwarding to the correct groups.
//
// Usage:
//
//	recv-test-frames -iface lo0 -port 9001 -groups ff02::0,ff02::1
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
)

func main() {
	iface := flag.String("iface", "lo0", "interface to join multicast groups on (lo on Linux)")
	port := flag.Int("port", 9001, "UDP port to listen on")
	groupsFlag := flag.String("groups", "ff02::0,ff02::1", "comma-separated multicast group addresses to join")
	flag.Parse()

	ifi, err := net.InterfaceByName(*iface)
	if err != nil {
		log.Fatalf("interface %q: %v", *iface, err)
	}

	groups := strings.Split(*groupsFlag, ",")
	done := make(chan struct{})

	for _, g := range groups {
		g = strings.TrimSpace(g)
		go func(group string) {
			addr := &net.UDPAddr{
				IP:   net.ParseIP(group),
				Port: *port,
			}
			if addr.IP == nil {
				log.Printf("invalid group address %q, skipping", group)
				return
			}
			conn, err := net.ListenMulticastUDP("udp6", ifi, addr)
			if err != nil {
				log.Printf("join %s on %s: %v", group, ifi.Name, err)
				return
			}
			defer func() {
				if err := conn.Close(); err != nil {
					log.Printf("close conn %s: %v", group, err)
				}
			}()
			log.Printf("joined %-20s on %s", group, ifi.Name)
			recvLoop(conn, group)
		}(g)
	}

	<-done // block until interrupted
}

func recvLoop(conn *net.UDPConn, group string) {
	buf := make([]byte, 65536)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("[%s] read error: %v", group, err)
			return
		}
		f, err := frame.Decode(buf[:n])
		if err != nil {
			log.Printf("[%s] decode error from %s: %v", group, src, err)
			continue
		}
		fmt.Printf("recv  group=%-22s  src=%-26s  txid[0:4]=%08X  payload_len=%d\n",
			group, src, binary.BigEndian.Uint32(f.TxID[0:4]), len(f.Payload))
	}
}
