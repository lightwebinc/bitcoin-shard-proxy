// Package worker implements the per-CPU receive-and-retransmit loop for
// bitcoin-shard-proxy.
//
// # Design
//
// Each Worker owns two UDP sockets:
//
//   - An ingress socket bound to the listen address using SO_REUSEPORT.
//     The kernel load-balances incoming datagrams across all workers that
//     share the same port, distributing receive load without any userspace
//     coordination or channel passing.
//
//   - An egress socket bound to an ephemeral port, used exclusively for
//     WriteTo calls targeting the derived multicast group address.
//
// The hot path — [Worker.process] — performs zero heap allocation in the
// common case: it decodes the frame into a stack-allocated pointer to the
// receive buffer, computes the group index with a two-instruction shift and
// mask, constructs the destination address, and calls WriteTo with the
// original raw datagram bytes. No re-serialisation occurs; the frame is
// forwarded verbatim so subscribers receive the full BSV frame including
// the original txid.
//
// # SO_REUSEPORT
//
// SO_REUSEPORT (Linux 3.9+) allows multiple sockets to bind to the same
// address and port. The kernel hashes the 4-tuple of the incoming datagram
// to select which socket — and therefore which worker goroutine — receives
// each packet. This provides CPU-local receive processing with no shared
// data structures on the ingress path.
package worker

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

const (
	// RecvBufSize is the per-worker datagram read buffer in bytes.
	// 64 KiB covers jumbo-frame paths; individual BSV transactions sent over
	// UDP should stay well below the path MTU to avoid IP fragmentation.
	RecvBufSize = 65536

	// socketBufBytes is the value requested for SO_RCVBUF and SO_SNDBUF.
	// Larger buffers absorb short-lived bursts without dropping datagrams.
	socketBufBytes = 4 * 1024 * 1024 // 4 MiB
)

// Worker owns one ingress socket and one egress socket and processes
// datagrams on a single goroutine. Create with [New] and start with [Run].
type Worker struct {
	id     int
	engine *shard.Engine
	iface  *net.Interface
	port   int // egress UDP destination port for multicast groups
	debug  bool
	log    *slog.Logger
}

// New constructs a Worker. No sockets are opened until [Run] is called.
//
//   - id is a small integer used in log fields to distinguish workers.
//   - engine is the shared, immutable shard derivation engine.
//   - iface is the NIC over which multicast datagrams are sent.
//   - egressPort is the UDP destination port written into outgoing datagrams.
//   - debug enables per-packet debug logging and multicast loopback.
func New(id int, engine *shard.Engine, iface *net.Interface, egressPort int, debug bool) *Worker {
	return &Worker{
		id:     id,
		engine: engine,
		iface:  iface,
		port:   egressPort,
		debug:  debug,
		log:    slog.Default().With("worker", id),
	}
}

// Run opens the ingress and egress sockets and enters the receive loop.
// It blocks until done is closed or an unrecoverable socket error occurs.
// Intended to be launched as a goroutine from main.
//
//   - listenAddr is the bind address string (e.g. "[::]"), without port.
//   - listenPort is the UDP port shared by all workers via SO_REUSEPORT.
//   - done is closed by the main goroutine to signal graceful shutdown.
func (w *Worker) Run(listenAddr string, listenPort int, done <-chan struct{}) error {
	// Open a raw IPv6 UDP socket so we can set SO_REUSEPORT before binding.
	// net.ListenPacket does not expose this option.
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM, unix.IPPROTO_UDP)
	if err != nil {
		return fmt.Errorf("worker %d: socket: %w", w.id, err)
	}

	// SO_REUSEPORT: allow all worker sockets to share the same port.
	// The kernel distributes incoming datagrams across them.
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("worker %d: SO_REUSEPORT: %w", w.id, err)
	}

	// Enlarge the receive buffer to absorb bursts of transaction datagrams.
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, socketBufBytes); err != nil {
		w.log.Warn("could not set SO_RCVBUF", "err", err)
	}

	sa := &unix.SockaddrInet6{Port: listenPort}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("worker %d: bind :%d: %w", w.id, listenPort, err)
	}

	// Wrap the raw fd in a net.PacketConn for idiomatic Read/Write calls.
	// os.NewFile duplicates the fd internally; close the original.
	file := os.NewFile(uintptr(fd), fmt.Sprintf("udp6-ingress-w%d", w.id))
	conn, err := net.FilePacketConn(file)
	_ = file.Close()
	if err != nil {
		return fmt.Errorf("worker %d: FilePacketConn: %w", w.id, err)
	}
	defer conn.Close()

	// Close the ingress socket when done is signalled, unblocking ReadFrom.
	go func() {
		<-done
		conn.Close()
	}()

	// WriteTo is used for each outgoing multicast datagram.
	egressConn, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		return fmt.Errorf("worker %d: egress ListenPacket: %w", w.id, err)
	}
	defer egressConn.Close()

	udpEgress := egressConn.(*net.UDPConn)

	// Bind egress socket to configured NIC so multicast datagrams
	// exit on correct interface.
	egressFile, err := udpEgress.File()
	if err != nil {
		return fmt.Errorf("worker %d: get UDP file descriptor: %w", w.id, err)
	}
	defer egressFile.Close()

	if err := unix.SetsockoptInt(int(egressFile.Fd()), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, w.iface.Index); err != nil {
		return fmt.Errorf("worker %d: SetMulticastInterface(%s): %w", w.id, w.iface.Name, err)
	}

	// Disable multicast loopback in production so the proxy does not receive
	// its own forwarded datagrams. Enable in debug mode for single-host testing.
	loopback := 0
	if w.debug {
		loopback = 1
	}
	if err := unix.SetsockoptInt(int(egressFile.Fd()), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_LOOP, loopback); err != nil {
		w.log.Warn("could not configure multicast loopback", "err", err)
	}

	buf := make([]byte, RecvBufSize)
	w.log.Info("ready",
		"shard_bits", w.engine.ShardBits(),
		"num_groups", w.engine.NumGroups(),
		"listen_port", listenPort,
	)

	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if isClosedErr(err) {
				return nil
			}
			w.log.Warn("ReadFrom error", "err", err)
			continue
		}

		w.process(udpEgress, buf[:n])
	}
}

// process is the hot path executed for every received datagram.
//
// It decodes the BSV frame header, derives the destination multicast group
// address from the txid prefix, and retransmits the original raw datagram
// bytes verbatim to that address. No re-serialisation occurs.
func (w *Worker) process(egress *net.UDPConn, raw []byte) {
	f, err := frame.Decode(raw)
	if err != nil {
		w.log.Debug("frame decode error", "err", err, "len", len(raw))
		return
	}

	groupIdx := w.engine.GroupIndex(&f.TxID)
	dst := w.engine.Addr(groupIdx, w.port)

	if _, err := egress.WriteTo(raw, dst); err != nil {
		w.log.Warn("WriteTo error", "dst", dst, "err", err)
		return
	}

	if w.debug {
		w.log.Debug("forwarded",
			"txid_prefix", fmt.Sprintf("%08X", groupIdx),
			"group_idx", groupIdx,
			"dst", dst,
		)
	}
}

// isClosedErr returns true for errors that indicate the socket was closed
// deliberately (e.g. as part of graceful shutdown), as opposed to errors
// that should be logged as unexpected.
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	return isErrno(err, syscall.EBADF) || isErrno(err, syscall.EINVAL)
}

// isErrno unwraps err and reports whether its innermost value is target.
func isErrno(err error, target syscall.Errno) bool {
	for err != nil {
		if e, ok := err.(syscall.Errno); ok {
			return e == target
		}
		err = unwrap(err)
	}
	return false
}

// unwrap returns the next error in the chain, or nil.
func unwrap(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}
