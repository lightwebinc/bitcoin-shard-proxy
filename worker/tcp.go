// Package worker — tcp.go provides TCPIngress: a TCP listener that accepts
// reliable frame delivery connections and feeds them into the shared Forwarder.
//
// # Protocol
//
// Each TCP connection carries a stream of v2 frames with no framing envelope:
//
//  1. Client writes the 84-byte v2 header.
//  2. Client writes PayLen bytes of payload (PayLen read from header @80–83).
//  3. Repeat for each subsequent frame.
//
// The proxy reads exactly two buffers per frame (header then payload) using
// [io.ReadFull]. A [bufio.Reader] (64 KiB) absorbs kernel round-trips under
// burst load.
//
// v1 frames are rejected (ErrBadVer from frame.Decode) and the connection is
// closed.
package worker

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/jefflightweb/bitcoin-shard-proxy/forwarder"
	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/metrics"
)

const tcpBufSize = 64 * 1024 // 64 KiB read buffer per TCP connection

// TCPIngress listens for TCP connections carrying a stream of v2 frames and
// forwards each frame via the shared [forwarder.Forwarder].
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

	for {
		conn, err := ln.Accept()
		if err != nil {
			if isClosedErr(err) {
				return nil
			}
			ti.log.Warn("Accept error", "err", err)
			continue
		}
		go ti.handleConn(conn, targets)
	}
}

// handleConn reads a stream of v2 frames from conn and forwards each one.
// The connection is closed on any read error or protocol violation.
// Each goroutine owns its own encode and assembly buffers.
func (ti *TCPIngress) handleConn(conn net.Conn, targets []forwarder.Target) {
	defer conn.Close()
	remote := conn.RemoteAddr()
	ti.log.Debug("TCP connection accepted", "remote", remote)

	br := bufio.NewReaderSize(conn, tcpBufSize)
	hdr := make([]byte, frame.HeaderSize)
	connEncodeBuf := make([]byte, frame.HeaderSize+frame.MaxPayload)
	encodeBuf := make([]byte, frame.HeaderSize+frame.MaxPayload)

	for {
		// Read the fixed-size v2 header.
		if _, err := io.ReadFull(br, hdr); err != nil {
			if err != io.EOF && !isClosedErr(err) {
				ti.log.Debug("TCP read header error", "remote", remote, "err", err)
			}
			return
		}

		// Validate magic and frame version before reading the payload.
		// A full Decode is performed after reassembly; this early check avoids
		// reading an arbitrary PayLen from an invalid/misrouted connection.
		if hdr[0] != 0xE3 || hdr[1] != 0xE1 || hdr[2] != 0xF3 || hdr[3] != 0xE8 {
			ti.log.Warn("TCP bad magic; closing connection", "remote", remote)
			return
		}
		if hdr[6] != frame.FrameVerV2 {
			ti.log.Warn("TCP unsupported frame version; closing connection",
				"remote", remote, "ver", hdr[6])
			return
		}

		// PayLen is at bytes 80–83.
		payLen := int(uint32(hdr[80])<<24 | uint32(hdr[81])<<16 | uint32(hdr[82])<<8 | uint32(hdr[83]))
		if payLen > frame.MaxPayload {
			ti.log.Warn("TCP PayLen exceeds MaxPayload; closing connection",
				"remote", remote, "pay_len", payLen)
			return
		}

		// Assemble full frame into connEncodeBuf (header + payload contiguous).
		copy(connEncodeBuf, hdr)
		if payLen > 0 {
			if _, err := io.ReadFull(br, connEncodeBuf[frame.HeaderSize:frame.HeaderSize+payLen]); err != nil {
				ti.log.Debug("TCP read payload error", "remote", remote, "err", err)
				return
			}
		}

		ti.fwd.Process(targets, encodeBuf, connEncodeBuf[:frame.HeaderSize+payLen], remote, -1)
	}
}
