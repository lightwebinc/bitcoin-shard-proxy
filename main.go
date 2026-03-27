// Command bitcoin-shard-proxy accepts BSV transaction datagrams on a UDP
// IPv6 socket, derives a multicast group address from the transaction ID's
// top N bits, and retransmits each datagram verbatim to the derived group.
//
// Multiple worker goroutines — one per CPU by default — each bind an
// independent SO_REUSEPORT socket to the listen port. The kernel distributes
// incoming datagrams across them, providing CPU-local processing with no
// userspace coordination on the ingress path.
//
// # Quick start
//
//	bitcoin-shard-proxy -iface eth0 -shard-bits 16 -scope site
//
// # Configuration
//
// All flags have environment variable equivalents; see [config.Load] for the
// full mapping. The most important parameters:
//
//   - -shard-bits (SHARD_BITS): controls how many bits of the txid prefix
//     are used as the multicast group key. Range 1–24.
//     8  →   256 groups (fits any managed switch)
//     16 → 65536 groups (standard deployment)
//     24 → 16M   groups (maximum precision)
//
//   - -scope (MC_SCOPE): multicast scope. Use "site" for closed subscriber
//     fabrics; "global" only if subscribers span BGP domains.
//
//   - -iface (MULTICAST_IF): the NIC over which multicast datagrams are sent.
//     Must exist on the host; the proxy exits immediately if not found.
//
// # Graceful shutdown
//
// The proxy catches SIGINT (Ctrl-C) and SIGTERM (sent by systemd, container
// orchestrators, etc.). On receipt it logs the signal, closes the done
// channel, and waits for all workers to drain before exiting.
package main

import (
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jefflightweb/bitcoin-shard-proxy/config"
	"github.com/jefflightweb/bitcoin-shard-proxy/shard"
	"github.com/jefflightweb/bitcoin-shard-proxy/worker"
)

func main() {
	// Load and validate configuration from flags / environment variables.
	cfg, err := config.Load()
	if err != nil {
		// Use plain stderr before the structured logger is initialised.
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	// Initialise the structured logger. Debug level enables per-packet output.
	logLevel := slog.LevelInfo
	if cfg.Debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Resolve the egress network interface once; workers share the *net.Interface.
	iface, err := net.InterfaceByName(cfg.MulticastIF)
	if err != nil {
		slog.Error("multicast interface not found", "iface", cfg.MulticastIF, "err", err)
		os.Exit(1)
	}

	// Construct the shard engine. It is immutable and safe for concurrent use.
	engine := shard.New(cfg.MCPrefix, cfg.MCMiddleBytes, cfg.ShardBits)

	slog.Info("bitcoin-shard-proxy starting",
		"workers", cfg.NumWorkers,
		"shard_bits", cfg.ShardBits,
		"num_groups", engine.NumGroups(),
		"scope", cfg.MCScope,
		"listen_port", cfg.ListenPort,
		"egress_port", cfg.EgressPort,
		"iface", cfg.MulticastIF,
		"debug", cfg.Debug,
	)

	// done is closed to signal all workers to stop their receive loops.
	done := make(chan struct{})
	var wg sync.WaitGroup

	for i := range cfg.NumWorkers {
		w := worker.New(i, engine, iface, cfg.EgressPort, cfg.Debug)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Run(cfg.ListenAddr, cfg.ListenPort, done); err != nil {
				slog.Error("worker exited with error", "worker", i, "err", err)
			}
		}()
	}

	// ── Signal handling ───────────────────────────────────────────────────
	//
	// sig is a buffered channel of capacity 1. The buffer is intentional:
	// if a signal arrives in the brief window between signal.Notify and the
	// <-sig receive below, the runtime deposits it into the buffer rather
	// than dropping it. Without the buffer, that race would cause the signal
	// to be silently lost and the proxy would never shut down.
	//
	// signal.Notify registers sig with the Go runtime's signal dispatcher.
	// From this point, any SIGINT (Ctrl-C) or SIGTERM sent to the process
	// causes the runtime to write the signal value into sig.
	//
	// <-sig is a blocking channel receive. It suspends the main goroutine
	// here — the proxy is running, workers are processing packets — until
	// a value arrives in the channel. The received value is captured (not
	// discarded) so it can be included in the shutdown log line.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	received := <-sig // block until SIGINT or SIGTERM

	slog.Info("received signal, shutting down",
		"signal", received,
		"signal_number", int(received.(syscall.Signal)),
	)

	// Close done to unblock all worker receive loops, then wait for them to
	// finish draining any in-flight datagrams.
	close(done)
	wg.Wait()

	slog.Info("all workers stopped; exiting cleanly")
}
