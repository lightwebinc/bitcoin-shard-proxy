// Package config loads and validates runtime configuration for
// bitcoin-shard-proxy. Parameters are accepted from CLI flags first;
// environment variables serve as fallbacks; hard-coded defaults apply when
// neither is present.
//
// # Environment variable mapping
//
//	Flag             Env var          Default       Description
//	-listen          LISTEN_ADDR      [::]          Ingress bind address
//	-listen-port     LISTEN_PORT      9000          UDP listen port
//	-iface           MULTICAST_IF     eth0          NIC for multicast egress
//	-egress-port     EGRESS_PORT      9001          Destination port on groups
//	-shard-bits      SHARD_BITS       2             Key bit width (1–24)
//	-scope           MC_SCOPE         site          Multicast scope
//	-workers         NUM_WORKERS      runtime.NumCPU  Worker goroutine count
//	-debug           —                false         Per-packet logging + loopback
//	-metrics-addr    METRICS_ADDR     :9100         HTTP bind for /metrics, /healthz, /readyz
//	-instance        INSTANCE_ID      hostname      OTel service.instance.id
//	-otlp-endpoint   OTLP_ENDPOINT    ""            OTLP gRPC endpoint (empty = disabled)
//	-otlp-interval   OTLP_INTERVAL    30s           OTLP push interval
package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"time"
)

// Scopes maps a human-readable scope name to the two-byte big-endian IPv6
// multicast prefix. See RFC 4291 §2.7.
//
//	"link"   → FF02::/16  link-local   (does not cross routers)
//	"site"   → FF05::/16  site-local   (recommended default for closed fabrics)
//	"org"    → FF08::/16  org-local
//	"global" → FF0E::/16  global       (routable across BGP domains)
var Scopes = map[string]uint16{
	"link":   0xFF02,
	"site":   0xFF05,
	"org":    0xFF08,
	"global": 0xFF0E,
}

// Config holds all runtime parameters for the proxy. Fields are read-only
// after [Load] returns; treat the value as immutable.
type Config struct {
	// Network
	ListenAddr  string // e.g. "[::]"
	ListenPort  int    // UDP port to receive BSV transaction frames
	MulticastIF string // NIC name for multicast egress, e.g. "eth0"
	EgressPort  int    // Destination UDP port written into outgoing multicast datagrams

	// Sharding
	ShardBits     uint     // Number of txid prefix bits used as the group key (1–24)
	NumGroups     uint32   // Derived: 1 << ShardBits — total distinct multicast groups
	MCScope       string   // Human name; one of the keys in Scopes
	MCPrefix      uint16   // Derived from MCScope — upper 16 bits of the IPv6 group address
	MCBaseAddr    string   // Base IPv6 address for assigned address space (bytes 2-12)
	MCMiddleBytes [11]byte // Derived from MCBaseAddr — bytes 2-12 of multicast address

	// Runtime
	NumWorkers int  // Worker goroutine count; defaults to runtime.NumCPU()
	Debug      bool // Enables per-packet debug logging and multicast loopback

	// Observability
	MetricsAddr  string        // HTTP bind address for /metrics, /healthz, /readyz
	InstanceID   string        // OTel service.instance.id for federation; defaults to hostname
	OTLPEndpoint string        // gRPC OTLP endpoint (empty = disabled)
	OTLPInterval time.Duration // OTLP push interval
}

// Load parses flags and environment variables, validates all values, and
// returns a populated [Config]. It calls [flag.Parse] internally; callers
// must not call flag.Parse separately.
//
// Returns an error if any value is out of range or the specified network
// interface does not exist on this host.
func Load() (*Config, error) {
	c := &Config{}

	flag.StringVar(&c.ListenAddr, "listen", envStr("LISTEN_ADDR", "[::]"),
		"ingress bind address (without port)")
	flag.IntVar(&c.ListenPort, "listen-port", envInt("LISTEN_PORT", 9000),
		"UDP listen port for incoming BSV transaction frames")
	flag.StringVar(&c.MulticastIF, "iface", envStr("MULTICAST_IF", "eth0"),
		"network interface name for multicast egress")
	flag.IntVar(&c.EgressPort, "egress-port", envInt("EGRESS_PORT", 9001),
		"destination UDP port written into outgoing multicast datagrams")
	flag.IntVar(&c.NumWorkers, "workers", envInt("NUM_WORKERS", runtime.NumCPU()),
		"number of worker goroutines (0 = runtime.NumCPU)")
	flag.StringVar(&c.MCScope, "scope", envStr("MC_SCOPE", "site"),
		"multicast scope: link | site | org | global")
	flag.StringVar(&c.MCBaseAddr, "mc-base-addr", envStr("MC_BASE_ADDR", ""),
		"base IPv6 address for assigned multicast address space (bytes 2-12)")
	flag.BoolVar(&c.Debug, "debug", false,
		"enable per-packet debug logging and multicast loopback (single-host testing)")
	flag.StringVar(&c.MetricsAddr, "metrics-addr", envStr("METRICS_ADDR", ":9100"),
		"HTTP bind address for /metrics, /healthz, /readyz")
	flag.StringVar(&c.InstanceID, "instance", envStr("INSTANCE_ID", ""),
		"OTel service.instance.id for federation (default: hostname)")
	flag.StringVar(&c.OTLPEndpoint, "otlp-endpoint", envStr("OTLP_ENDPOINT", ""),
		"OTLP gRPC endpoint for metric push (empty = disabled)")

	otlpInterval := flag.Duration("otlp-interval", envDuration("OTLP_INTERVAL", 30*time.Second),
		"OTLP push interval")

	bits := flag.Uint("shard-bits", uint(envInt("SHARD_BITS", 2)),
		"txid prefix bit width used as the shard key (1–24)")

	flag.Parse()

	// Validate shard bit width.
	if *bits < 1 || *bits > 24 {
		return nil, fmt.Errorf("shard-bits must be in [1, 24], got %d", *bits)
	}
	c.ShardBits = *bits
	c.NumGroups = 1 << c.ShardBits
	c.OTLPInterval = *otlpInterval

	// Resolve multicast scope.
	prefix, ok := Scopes[c.MCScope]
	if !ok {
		return nil, fmt.Errorf("unknown scope %q; valid values: link, site, org, global", c.MCScope)
	}
	c.MCPrefix = prefix

	// Parse base IPv6 address for middle bytes if provided.
	if c.MCBaseAddr != "" {
		ip := net.ParseIP(c.MCBaseAddr)
		if ip == nil {
			return nil, fmt.Errorf("invalid base IPv6 address %q", c.MCBaseAddr)
		}
		// Ensure we have a 16-byte IPv6 address
		ip16 := ip.To16()
		if ip16 == nil {
			return nil, fmt.Errorf("base address must be a valid 16-byte IPv6 address, got %q", c.MCBaseAddr)
		}
		// Check if it's actually IPv6 (not IPv4-mapped)
		if ip.To4() != nil {
			return nil, fmt.Errorf("base address must be IPv6, got IPv4 address %q", c.MCBaseAddr)
		}
		// Extract bytes 2-12 (11 bytes) for the middle section
		copy(c.MCMiddleBytes[:], ip16[2:13])
	} else {
		// Default to all zeros for backward compatibility
		for i := range c.MCMiddleBytes {
			c.MCMiddleBytes[i] = 0
		}
	}

	// Default workers to NumCPU if the flag or env was set to zero.
	if c.NumWorkers <= 0 {
		c.NumWorkers = runtime.NumCPU()
	}

	// Confirm the egress interface exists before the workers try to use it.
	if _, err := net.InterfaceByName(c.MulticastIF); err != nil {
		return nil, fmt.Errorf("multicast interface %q not found: %w", c.MulticastIF, err)
	}

	return c, nil
}

// envStr returns the value of environment variable key, or def if unset or empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt returns the integer value of environment variable key, or def if
// the variable is unset, empty, or not parseable as a base-10 integer.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envDuration returns the time.Duration value of environment variable key,
// or def if the variable is unset, empty, or not parseable.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
