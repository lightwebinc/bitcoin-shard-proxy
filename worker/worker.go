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
	"github.com/jefflightweb/bitcoin-shard-proxy/metrics"
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

// egressTarget pairs a network interface with its pre-opened egress socket.
type egressTarget struct {
	iface *net.Interface
	conn  *net.UDPConn
}

// Worker owns one ingress socket and one or more egress sockets and processes
// datagrams on a single goroutine. Create with [New] and start with [Run].
type Worker struct {
	id     int
	engine *shard.Engine
	ifaces []*net.Interface
	port   int // egress UDP destination port for multicast groups
	debug  bool
	rec    *metrics.Recorder
	log    *slog.Logger
}

// New constructs a Worker. No sockets are opened until [Run] is called.
//
//   - id is a small integer used in log fields to distinguish workers.
//   - engine is the shared, immutable shard derivation engine.
//   - ifaces is the list of NICs over which multicast datagrams are sent.
//   - egressPort is the UDP destination port written into outgoing datagrams.
//   - debug enables per-packet debug logging and multicast loopback.
//   - rec is the shared metrics recorder; may be nil to disable metrics.
func New(id int, engine *shard.Engine, ifaces []*net.Interface, egressPort int, debug bool, rec *metrics.Recorder) *Worker {
	return &Worker{
		id:     id,
		engine: engine,
		ifaces: ifaces,
		port:   egressPort,
		debug:  debug,
		rec:    rec,
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
	if actual, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF); err == nil {
		w.log.Debug("SO_RCVBUF", "requested_bytes", socketBufBytes, "actual_bytes", actual)
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
	defer func() {
		if err := conn.Close(); err != nil {
			w.log.Warn("close ingress conn", "err", err)
		}
	}()

	// Close the ingress socket when done is signalled, unblocking ReadFrom.
	go func() {
		<-done
		if err := conn.Close(); err != nil {
			w.log.Warn("close ingress conn on shutdown", "err", err)
		}
	}()

	// Open one egress socket per configured interface.
	loopback := 0
	if w.debug {
		loopback = 1
	}
	targets := make([]egressTarget, 0, len(w.ifaces))
	for _, iface := range w.ifaces {
		egressConn, err := net.ListenPacket("udp6", "[::]:0")
		if err != nil {
			return fmt.Errorf("worker %d: egress ListenPacket(%s): %w", w.id, iface.Name, err)
		}
		udpEgress := egressConn.(*net.UDPConn)

		rawConn, err := udpEgress.SyscallConn()
		if err != nil {
			_ = udpEgress.Close()
			return fmt.Errorf("worker %d: SyscallConn(%s): %w", w.id, iface.Name, err)
		}
		var setsockoptErr error
		if err := rawConn.Control(func(fd uintptr) {
			if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, iface.Index); err != nil {
				setsockoptErr = fmt.Errorf("IPV6_MULTICAST_IF: %w", err)
				return
			}
			if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_LOOP, loopback); err != nil {
				w.log.Warn("could not configure multicast loopback", "iface", iface.Name, "err", err)
			}
		}); err != nil {
			_ = udpEgress.Close()
			return fmt.Errorf("worker %d: rawConn.Control(%s): %w", w.id, iface.Name, err)
		}
		if setsockoptErr != nil {
			_ = udpEgress.Close()
			return fmt.Errorf("worker %d: SetMulticastInterface(%s): %w", w.id, iface.Name, setsockoptErr)
		}

		// Probe the socket on worker 0 only — all workers use the same
		// interfaces, so one probe per interface at startup is sufficient.
		if w.id == 0 {
			if err := probeEgressSocket(w.log, udpEgress, iface); err != nil {
				_ = udpEgress.Close()
				return fmt.Errorf("worker %d: egress probe(%s): %w", w.id, iface.Name, err)
			}
		}

		targets = append(targets, egressTarget{iface: iface, conn: udpEgress})
	}
	defer func() {
		for _, tgt := range targets {
			if err := tgt.conn.Close(); err != nil {
				w.log.Warn("close egress conn", "iface", tgt.iface.Name, "err", err)
			}
		}
	}()

	buf := make([]byte, RecvBufSize)
	ifaceNames := make([]string, len(targets))
	for i, tgt := range targets {
		ifaceNames[i] = tgt.iface.Name
	}
	w.log.Info("ready",
		"shard_bits", w.engine.ShardBits(),
		"num_groups", w.engine.NumGroups(),
		"listen_port", listenPort,
		"egress_ifaces", ifaceNames,
	)
	if w.rec != nil {
		w.rec.WorkerReady()
		defer w.rec.WorkerDone()
	}

	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if isClosedErr(err) {
				return nil
			}
			w.log.Warn("ReadFrom error", "err", err)
			if w.rec != nil {
				w.rec.IngressError(targets[0].iface.Name, w.id)
			}
			continue
		}

		if n == RecvBufSize {
			w.log.Warn("datagram fills recv buffer; may be truncated",
				"src", src, "len", n)
			if w.rec != nil {
				w.rec.PacketDropped(targets[0].iface.Name, w.id, "truncated")
			}
			continue
		}

		if w.rec != nil {
			w.rec.PacketReceived(targets[0].iface.Name, w.id, n)
		}
		w.process(targets, buf[:n], src)
	}
}

// process is the hot path executed for every received datagram.
//
// It decodes the BSV frame header, derives the destination multicast group
// address from the txid prefix, and retransmits the original raw datagram
// bytes verbatim to that address on every configured egress interface.
// No re-serialisation occurs.
func (w *Worker) process(targets []egressTarget, raw []byte, src net.Addr) {
	f, err := frame.Decode(raw)
	if err != nil {
		w.log.Debug("frame decode error", "err", err, "len", len(raw))
		if w.rec != nil {
			w.rec.PacketDropped(targets[0].iface.Name, w.id, "decode_error")
		}
		return
	}

	groupIdx := w.engine.GroupIndex(&f.TxID)
	dst := w.engine.Addr(groupIdx, w.port)

	for _, tgt := range targets {
		dst.Zone = tgt.iface.Name
		if _, err := tgt.conn.WriteTo(raw, dst); err != nil {
			w.log.Warn("WriteTo error", "iface", tgt.iface.Name, "dst", dst, "err", err)
			if w.rec != nil {
				w.rec.PacketDropped(tgt.iface.Name, w.id, "write_error")
				w.rec.EgressError(tgt.iface.Name, w.id)
			}
			continue
		}
		if w.rec != nil {
			w.rec.PacketForwarded(tgt.iface.Name, w.id, groupIdx, len(raw))
		}
	}

	if w.debug {
		w.log.Debug("forwarded",
			"txid_prefix", fmt.Sprintf("%08X", groupIdx),
			"group_idx", groupIdx,
			"src", src,
			"dst", dst,
		)
	}
}

// probeEgressSocket sends a zero-byte datagram to the link-local all-nodes
// multicast address (ff02::1) on the given interface. This exercises the
// actual WriteTo path with IPV6_MULTICAST_IF applied and catches interface
// misconfigurations — missing routes, policy drops, unsupported tunnel types —
// at startup rather than silently under load.
//
// Hard errors (EPERM, EADDRNOTAVAIL) cause a non-nil return so the caller
// can fail fast. ENETUNREACH is treated as a warning — the route may not
// exist yet (e.g. added after startup) but the socket is otherwise valid.
// Any other error is also treated as a soft warning.
func probeEgressSocket(log *slog.Logger, conn *net.UDPConn, iface *net.Interface) error {
	dst := &net.UDPAddr{
		IP:   net.ParseIP("ff02::1"),
		Port: 9, // discard port — no listener required
	}
	_, err := conn.WriteTo([]byte{}, dst)
	if err == nil {
		return nil
	}
	if isErrno(err, syscall.EPERM) ||
		isErrno(err, syscall.EADDRNOTAVAIL) {
		return fmt.Errorf("interface not usable for multicast egress: %w", err)
	}
	// Soft: ENETUNREACH (missing route) and other errors are warnings only.
	log.Warn("egress probe warning", "iface", iface.Name, "err", err)
	return nil
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
