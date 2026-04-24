// Package worker — tcp.go provides TCPIngress: a TCP listener that accepts
// reliable frame delivery connections and feeds them into the shared Forwarder.
//
// # Protocol
//
// Each TCP connection carries a stream of v1 or BRC-122 frames with no framing
// envelope. The proxy reads the minimum header first (44 bytes for v1,
// extended to 92 for BRC-122), then reads the declared payload:
//
//  1. Read [frame.HeaderSizeLegacy] (44) bytes — enough to see the version byte
//     and, for v1, the PayLen field.
//  2. If FrameVer == BRC-122: read 48 more bytes to complete the 92-byte header
//     (bytes 44–91), which includes the 4-byte PayLen field at bytes 88–91.
//  3. Read PayLen bytes of payload.
//  4. Forward assembled frame to [forwarder.Forwarder.Process].
//
// A [bufio.Reader] (64 KiB) absorbs kernel round-trips under burst load.
// v1 and BRC-122 frames are forwarded verbatim.
package worker

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/lightwebinc/bitcoin-shard-common/frame"
	"github.com/lightwebinc/bitcoin-shard-proxy/forwarder"
	"github.com/lightwebinc/bitcoin-shard-proxy/metrics"
)

const tcpBufSize = 64 * 1024 // 64 KiB read buffer per TCP connection

// TCPIngress listens for TCP connections carrying a stream of v1 or BRC-122 frames
// and forwards each frame via the shared [forwarder.Forwarder].
type TCPIngress struct {
	fwd    *forwarder.Forwarder
	ifaces []*net.Interface
	rec    *metrics.Recorder
	log    *slog.Logger
}

// NewTCPIngress constructs a TCPIngress. No sockets are opened until [Run] is
// called.
func NewTCPIngress(fwd *forwarder.Forwarder, ifaces []*net.Interface, rec *metrics.Recorder) *TCPIngress {
	return &TCPIngress{
		fwd:    fwd,
		ifaces: ifaces,
		rec:    rec,
		log:    slog.Default().With("component", "tcp-ingress"),
	}
}

// Run starts the TCP accept loop on listenAddr:listenPort. It blocks until
// done is closed. Each accepted connection is handled in its own goroutine.
func (ti *TCPIngress) Run(listenAddr string, listenPort int, done <-chan struct{}) error {
	addr := fmt.Sprintf("%s:%d", listenAddr, listenPort)
	ln, err := net.Listen("tcp6", addr)
	if err != nil {
		return fmt.Errorf("tcp-ingress: listen %s: %w", addr, err)
	}

	// Close the listener when done is signalled, unblocking Accept.
	go func() {
		<-done
		_ = ln.Close()
	}()

	ti.log.Info("TCP ingress ready", "addr", ln.Addr())

	// Open a set of egress targets shared by all connections on this goroutine.
	// Worker 0 ownership is assumed (TCP ingress is a single listener).
	targets, err := ti.fwd.OpenTargets(ti.ifaces, true)
	if err != nil {
		return fmt.Errorf("tcp-ingress: open targets: %w", err)
	}
	defer forwarder.CloseTargets(targets, ti.log)

	var connWG sync.WaitGroup
	defer connWG.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if isClosedErr(err) {
				return nil
			}
			ti.log.Warn("Accept error", "err", err)
			continue
		}
		connWG.Add(1)
		go func() {
			defer connWG.Done()
			go func() {
				<-done
				_ = conn.Close()
			}()
			ti.handleConn(conn, targets)
		}()
	}
}

// handleConn reads a stream of v1 or BRC-122 frames from conn and forwards each.
// The connection is closed on any read error or protocol violation.
// Each goroutine owns its own encode and assembly buffers.
func (ti *TCPIngress) handleConn(conn net.Conn, targets []forwarder.Target) {
	defer func() { _ = conn.Close() }()
	remote := conn.RemoteAddr()
	ti.log.Debug("TCP connection accepted", "remote", remote)

	br := bufio.NewReaderSize(conn, tcpBufSize)
	connEncodeBuf := make([]byte, frame.HeaderSize+frame.MaxPayload)

	for {
		// Step 1: read the v1 minimum header (44 bytes). This covers both
		// v1 (complete header) and the leading 44 bytes of a BRC-122 header.
		if _, err := io.ReadFull(br, connEncodeBuf[:frame.HeaderSizeLegacy]); err != nil {
			if err != io.EOF && !isClosedErr(err) {
				ti.log.Debug("TCP read header error", "remote", remote, "err", err)
			}
			return
		}

		// Validate magic before reading further.
		if connEncodeBuf[0] != 0xE3 || connEncodeBuf[1] != 0xE1 ||
			connEncodeBuf[2] != 0xF3 || connEncodeBuf[3] != 0xE8 {
			ti.log.Warn("TCP bad magic; closing connection", "remote", remote)
			return
		}

		var hdrSize, payLen int
		switch connEncodeBuf[6] {
		case frame.FrameVerV1:
			hdrSize = frame.HeaderSizeLegacy
			payLen = int(uint32(connEncodeBuf[40])<<24 | uint32(connEncodeBuf[41])<<16 |
				uint32(connEncodeBuf[42])<<8 | uint32(connEncodeBuf[43]))
		case frame.FrameVerBRC122:
			// Step 2: read the remaining 48 bytes to complete the 92-byte BRC-122 header
			// (includes the 4-byte PayLen field at bytes 88–91).
			if _, err := io.ReadFull(br, connEncodeBuf[frame.HeaderSizeLegacy:frame.HeaderSize]); err != nil {
				ti.log.Debug("TCP read BRC-122 header extension error", "remote", remote, "err", err)
				return
			}
			hdrSize = frame.HeaderSize
			payLen = int(uint32(connEncodeBuf[88])<<24 | uint32(connEncodeBuf[89])<<16 |
				uint32(connEncodeBuf[90])<<8 | uint32(connEncodeBuf[91]))
		default:
			ti.log.Warn("TCP unsupported frame version; closing connection",
				"remote", remote, "ver", connEncodeBuf[6])
			return
		}

		if payLen > frame.MaxPayload {
			ti.log.Warn("TCP PayLen exceeds MaxPayload; closing connection",
				"remote", remote, "pay_len", payLen)
			return
		}

		// Step 3: read payload bytes.
		if payLen > 0 {
			if _, err := io.ReadFull(br, connEncodeBuf[hdrSize:hdrSize+payLen]); err != nil {
				ti.log.Debug("TCP read payload error", "remote", remote, "err", err)
				return
			}
		}

		ti.fwd.Process(targets, connEncodeBuf[:hdrSize+payLen], remote, -1)
	}
}
