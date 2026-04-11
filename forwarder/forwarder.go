// Package forwarder implements the decode → override → sequence → forward
// pipeline for bitcoin-shard-proxy.
//
// # Hot path
//
// [Forwarder.Process] decodes the v2 frame, optionally stamps static overrides
// and a proxy-assigned ShardSeqNum, then writes to every configured egress
// target. When no re-encoding is required (sender-assigned ShardSeqNum ≠ 0 and
// no static overrides active), the original raw bytes are forwarded verbatim
// via a zero-copy [net.UDPConn.WriteTo] call.
//
// # Egress socket lifecycle
//
// [Forwarder.OpenTargets] opens one UDP socket per interface with
// IPV6_MULTICAST_IF applied. Pass the returned slice to every [Forwarder.Process]
// call and release with [CloseTargets] during graceful shutdown.
package forwarder

import (
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/jefflightweb/bitcoin-shard-proxy/frame"
	"github.com/jefflightweb/bitcoin-shard-proxy/metrics"
	"github.com/jefflightweb/bitcoin-shard-proxy/sequence"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
)

// Target pairs a network interface with its pre-opened multicast egress socket.
type Target struct {
	Iface *net.Interface
	Conn  *net.UDPConn
}

// Forwarder decodes v2 ingress frames, applies static field overrides and
// proxy-assigned sequence numbers (when configured), then forwards each frame
// to all egress targets.
type Forwarder struct {
	engine              *shard.Engine
	counters            *sequence.Counters
	egressPort          int
	proxySeqEnabled     bool
	staticSubtreeID     []byte // nil = passthrough; len 32 = override all frames
	staticSubtreeHeight *uint8 // nil = passthrough; non-nil (incl. *0) = override all frames
	debug               bool
	rec                 *metrics.Recorder
	log                 *slog.Logger
}

// New creates a Forwarder. No sockets are opened here; call [OpenTargets] in
// each worker's Run loop.
//
//   - engine: immutable shard derivation engine.
//   - counters: per-shard atomic sequence counters; may be nil when proxySeqEnabled is false.
//   - egressPort: UDP destination port written into outgoing multicast datagrams.
//   - proxySeqEnabled: stamp ShardSeqNum when sender leaves it 0.
//   - staticSubtreeID: override SubtreeID on every frame (nil = passthrough).
//   - staticSubtreeHeight: override SubtreeHeight on every frame (nil = passthrough; *0 is valid).
//   - debug: enable per-packet debug logging.
//   - rec: metrics recorder; may be nil.
func New(
	engine *shard.Engine,
	counters *sequence.Counters,
	egressPort int,
	proxySeqEnabled bool,
	staticSubtreeID []byte,
	staticSubtreeHeight *uint8,
	debug bool,
	rec *metrics.Recorder,
) *Forwarder {
	return &Forwarder{
		engine:              engine,
		counters:            counters,
		egressPort:          egressPort,
		proxySeqEnabled:     proxySeqEnabled,
		staticSubtreeID:     staticSubtreeID,
		staticSubtreeHeight: staticSubtreeHeight,
		debug:               debug,
		rec:                 rec,
		log:                 slog.Default().With("component", "forwarder"),
	}
}

// OpenTargets opens one multicast egress UDP socket per interface. On worker 0
// (probeWorker == true) each socket is probed with a zero-byte send to verify
// multicast egress is functional.
//
// On error, all partially opened sockets are closed before returning.
func (fw *Forwarder) OpenTargets(ifaces []*net.Interface, probeWorker bool) ([]Target, error) {
	loopback := 0
	if fw.debug {
		loopback = 1
	}
	targets := make([]Target, 0, len(ifaces))
	for _, iface := range ifaces {
		conn, err := openEgressSocket(iface, loopback)
		if err != nil {
			closeTargets(targets, fw.log)
			return nil, fmt.Errorf("forwarder: open egress socket (%s): %w", iface.Name, err)
		}
		if probeWorker {
			if err := probeEgressSocket(fw.log, conn, iface); err != nil {
				_ = conn.Close()
				closeTargets(targets, fw.log)
				return nil, fmt.Errorf("forwarder: egress probe (%s): %w", iface.Name, err)
			}
		}
		targets = append(targets, Target{Iface: iface, Conn: conn})
	}
	return targets, nil
}

// CloseTargets closes all egress sockets opened by [OpenTargets].
func CloseTargets(targets []Target, log *slog.Logger) {
	closeTargets(targets, log)
}

func closeTargets(targets []Target, log *slog.Logger) {
	for _, t := range targets {
		if err := t.Conn.Close(); err != nil {
			log.Warn("close egress conn", "iface", t.Iface.Name, "err", err)
		}
	}
}

// Process is the hot path: decode raw, apply overrides, forward.
//
// encodeBuf is a caller-supplied scratch buffer used when re-encoding is
// required. It must be at least frame.HeaderSize + frame.MaxPayload bytes.
// workerID is used only for metrics labels.
func (fw *Forwarder) Process(targets []Target, encodeBuf []byte, raw []byte, src net.Addr, workerID int) {
	f, err := frame.Decode(raw)
	if err != nil {
		fw.log.Debug("frame decode error", "err", err, "len", len(raw))
		if fw.rec != nil && len(targets) > 0 {
			fw.rec.PacketDropped(targets[0].Iface.Name, workerID, "decode_error")
		}
		return
	}

	groupIdx := fw.engine.GroupIndex(&f.TxID)
	dst := fw.engine.Addr(groupIdx, fw.egressPort)

	// Apply static overrides; track whether re-encoding is required.
	needReencode := false
	if fw.staticSubtreeID != nil {
		copy(f.SubtreeID[:], fw.staticSubtreeID)
		needReencode = true
	}
	if fw.staticSubtreeHeight != nil {
		f.SubtreeHeight = *fw.staticSubtreeHeight
		needReencode = true
	}

	// Proxy-assigned sequence number (fallback path).
	if fw.proxySeqEnabled && f.ShardSeqNum == 0 {
		f.ShardSeqNum = fw.counters.Next(groupIdx)
		needReencode = true
	}

	for _, tgt := range targets {
		dst.Zone = tgt.Iface.Name
		var writeErr error
		if needReencode {
			n, err := frame.Encode(f, encodeBuf)
			if err != nil {
				fw.log.Error("frame encode error", "err", err)
				return
			}
			_, writeErr = tgt.Conn.WriteTo(encodeBuf[:n], dst)
		} else {
			_, writeErr = tgt.Conn.WriteTo(raw, dst)
		}
		if writeErr != nil {
			fw.log.Warn("WriteTo error", "iface", tgt.Iface.Name, "dst", dst, "err", writeErr)
			if fw.rec != nil {
				fw.rec.PacketDropped(tgt.Iface.Name, workerID, "write_error")
				fw.rec.EgressError(tgt.Iface.Name, workerID)
			}
			continue
		}
		if fw.rec != nil {
			fw.rec.PacketForwarded(tgt.Iface.Name, workerID, groupIdx, len(raw))
		}
	}

	if fw.debug {
		fw.log.Debug("forwarded",
			"txid_prefix", fmt.Sprintf("%08X", groupIdx),
			"group_idx", groupIdx,
			"seq", f.ShardSeqNum,
			"reencoded", needReencode,
			"src", src,
			"dst", dst,
		)
	}
}

// openEgressSocket opens a UDP6 socket with IPV6_MULTICAST_IF set to iface
// and IPV6_MULTICAST_LOOP set to loopback (1 for debug, 0 otherwise).
func openEgressSocket(iface *net.Interface, loopback int) (*net.UDPConn, error) {
	conn, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		return nil, err
	}
	udpConn := conn.(*net.UDPConn)

	rawConn, err := udpConn.SyscallConn()
	if err != nil {
		_ = udpConn.Close()
		return nil, err
	}
	var setsockoptErr error
	if ctrlErr := rawConn.Control(func(fd uintptr) {
		if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, iface.Index); err != nil {
			setsockoptErr = fmt.Errorf("IPV6_MULTICAST_IF: %w", err)
			return
		}
		if err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_LOOP, loopback); err != nil {
			slog.Default().Warn("could not configure multicast loopback", "iface", iface.Name, "err", err)
		}
	}); ctrlErr != nil {
		_ = udpConn.Close()
		return nil, ctrlErr
	}
	if setsockoptErr != nil {
		_ = udpConn.Close()
		return nil, setsockoptErr
	}
	return udpConn, nil
}

// probeEgressSocket sends a zero-byte datagram to ff02::1 (link-local
// all-nodes) on the given interface to verify the egress path at startup.
// Hard errors (EPERM, EADDRNOTAVAIL) are returned; other errors are warnings.
func probeEgressSocket(log *slog.Logger, conn *net.UDPConn, iface *net.Interface) error {
	dst := &net.UDPAddr{IP: net.ParseIP("ff02::1"), Port: 9}
	_, err := conn.WriteTo([]byte{}, dst)
	if err == nil {
		return nil
	}
	if isErrno(err, syscall.EPERM) || isErrno(err, syscall.EADDRNOTAVAIL) {
		return fmt.Errorf("interface not usable for multicast egress: %w", err)
	}
	log.Warn("egress probe warning", "iface", iface.Name, "err", err)
	return nil
}

func isErrno(err error, target syscall.Errno) bool {
	for err != nil {
		if e, ok := err.(syscall.Errno); ok {
			return e == target
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
		} else {
			break
		}
	}
	return false
}
