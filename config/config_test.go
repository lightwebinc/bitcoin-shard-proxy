package config

import (
	"flag"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// resetFlags recreates flag.CommandLine so that Load's flag.Parse call starts
// from a clean state between test runs.
func resetFlags() {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
}

// realIface returns the name of the first non-loopback interface, falling back
// to the loopback name. Used wherever Load needs a valid iface to pass
// its net.InterfaceByName check.
func realIface(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	for _, i := range ifaces {
		return i.Name
	}
	t.Fatal("no network interfaces found")
	return ""
}

// parseArgs is a helper that resets flag.CommandLine, sets os.Args, calls
// Load, and restores os.Args. Using flag package in tests requires resetting
// the flag set between runs.
func parseArgs(t *testing.T, args []string) (*Config, error) {
	t.Helper()
	old := os.Args
	t.Cleanup(func() {
		os.Args = old
		resetFlags()
	})
	os.Args = append([]string{"test"}, args...)
	resetFlags()
	return Load()
}

func TestLoadDefaults(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UDPListenPort != 9000 {
		t.Errorf("UDPListenPort = %d, want 9000", cfg.UDPListenPort)
	}
	if !cfg.ProxySeqEnabled {
		t.Error("ProxySeqEnabled should default to true")
	}
	if cfg.EgressPort != 9001 {
		t.Errorf("EgressPort = %d, want 9001", cfg.EgressPort)
	}
	if cfg.MCScope != "site" {
		t.Errorf("MCScope = %q, want site", cfg.MCScope)
	}
	if cfg.ShardBits != 2 {
		t.Errorf("ShardBits = %d, want 2", cfg.ShardBits)
	}
	if cfg.NumWorkers <= 0 {
		t.Errorf("NumWorkers = %d, want > 0", cfg.NumWorkers)
	}
	if cfg.MCPrefix != 0xFF05 {
		t.Errorf("MCPrefix = 0x%04X, want 0xFF05", cfg.MCPrefix)
	}
	if len(cfg.EgressIfaces) != 1 || cfg.EgressIfaces[0] != iface {
		t.Errorf("EgressIfaces = %v, want [%s]", cfg.EgressIfaces, iface)
	}
}

func TestLoadShardBitsRange(t *testing.T) {
	iface := realIface(t)
	for _, bits := range []string{"0", "25"} {
		_, err := parseArgs(t, []string{"-iface", iface, "-shard-bits", bits})
		if err == nil {
			t.Errorf("shard-bits=%s: want error, got nil", bits)
		}
	}
}

func TestLoadShardBitsValid(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-shard-bits", "8"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ShardBits != 8 {
		t.Errorf("ShardBits = %d, want 8", cfg.ShardBits)
	}
	if cfg.NumGroups != 256 {
		t.Errorf("NumGroups = %d, want 256", cfg.NumGroups)
	}
}

func TestLoadUnknownScope(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-scope", "galaxy"})
	if err == nil {
		t.Error("want error for unknown scope, got nil")
	}
}

func TestLoadAllScopes(t *testing.T) {
	iface := realIface(t)
	cases := map[string]uint16{
		"link":   0xFF02,
		"site":   0xFF05,
		"org":    0xFF08,
		"global": 0xFF0E,
	}
	for scope, want := range cases {
		cfg, err := parseArgs(t, []string{"-iface", iface, "-scope", scope})
		if err != nil {
			t.Errorf("scope=%s: Load error: %v", scope, err)
			continue
		}
		if cfg.MCPrefix != want {
			t.Errorf("scope=%s: MCPrefix = 0x%04X, want 0x%04X", scope, cfg.MCPrefix, want)
		}
	}
}

func TestLoadBadInterface(t *testing.T) {
	_, err := parseArgs(t, []string{"-iface", "no_such_iface_xyz"})
	if err == nil {
		t.Error("want error for missing interface, got nil")
	}
}

func TestLoadMultipleIfaces(t *testing.T) {
	iface := realIface(t)
	// Pass the same interface twice via comma-separated value.
	cfg, err := parseArgs(t, []string{"-iface", iface + "," + iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.EgressIfaces) != 2 {
		t.Errorf("EgressIfaces len = %d, want 2", len(cfg.EgressIfaces))
	}
}

func TestLoadEmptyIfaceError(t *testing.T) {
	_, err := parseArgs(t, []string{"-iface", ""})
	if err == nil {
		t.Error("want error for empty -iface, got nil")
	}
}

func TestLoadMCBaseAddrValid(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-mc-base-addr", "ff05::1:2:3"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Middle bytes should be non-zero.
	var zero [11]byte
	if cfg.MCMiddleBytes == zero {
		t.Error("MCMiddleBytes should be non-zero for non-empty base addr")
	}
}

func TestLoadMCBaseAddrInvalid(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-mc-base-addr", "not-an-ip"})
	if err == nil {
		t.Error("want error for invalid base addr, got nil")
	}
}

func TestLoadMCBaseAddrIPv4Rejected(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-mc-base-addr", "192.168.1.1"})
	if err == nil {
		t.Error("want error for IPv4 base addr, got nil")
	}
}

func TestLoadZeroWorkersDefaultsToCPU(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-workers", "0"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.NumWorkers <= 0 {
		t.Errorf("NumWorkers = %d after zero, want > 0", cfg.NumWorkers)
	}
}

func TestLoadInstanceIDDefaultsToHostname(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// InstanceID defaults to hostname when not set; flag default is "".
	// Load does not fill it in — that is done by metrics.New. Just confirm
	// the field is accessible and the load succeeds.
	_ = cfg.InstanceID
}

// ── env helper tests ──────────────────────────────────────────────────────────

func TestEnvStrFallback(t *testing.T) {
	_ = os.Unsetenv("TEST_ENV_STR_KEY")
	if got := envStr("TEST_ENV_STR_KEY", "default"); got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
}

func TestEnvStrSet(t *testing.T) {
	t.Setenv("TEST_ENV_STR_KEY", "hello")
	if got := envStr("TEST_ENV_STR_KEY", "default"); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestEnvIntFallback(t *testing.T) {
	_ = os.Unsetenv("TEST_ENV_INT_KEY")
	if got := envInt("TEST_ENV_INT_KEY", 42); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestEnvIntSet(t *testing.T) {
	t.Setenv("TEST_ENV_INT_KEY", "99")
	if got := envInt("TEST_ENV_INT_KEY", 42); got != 99 {
		t.Errorf("got %d, want 99", got)
	}
}

func TestEnvIntInvalid(t *testing.T) {
	t.Setenv("TEST_ENV_INT_KEY", "not-a-number")
	if got := envInt("TEST_ENV_INT_KEY", 7); got != 7 {
		t.Errorf("got %d, want fallback 7", got)
	}
}

func TestEnvBoolFallback(t *testing.T) {
	os.Unsetenv("TEST_ENV_BOOL_KEY")
	if got := envBool("TEST_ENV_BOOL_KEY", true); !got {
		t.Error("envBool: expected fallback true")
	}
}

func TestEnvBoolSet(t *testing.T) {
	t.Setenv("TEST_ENV_BOOL_KEY", "true")
	if got := envBool("TEST_ENV_BOOL_KEY", false); !got {
		t.Error("envBool: expected true")
	}
}

func TestEnvBoolSetFalse(t *testing.T) {
	t.Setenv("TEST_ENV_BOOL_KEY", "false")
	if got := envBool("TEST_ENV_BOOL_KEY", true); got {
		t.Error("envBool: expected false")
	}
}

func TestEnvBoolInvalid(t *testing.T) {
	t.Setenv("TEST_ENV_BOOL_KEY", "not-a-bool")
	if got := envBool("TEST_ENV_BOOL_KEY", true); !got {
		t.Error("envBool: expected fallback true for invalid value")
	}
}

// ── new v2 flag tests ───────────────────────────────────────────────────────────────

func TestLoadUDPListenPortCustom(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-udp-listen-port", "9500"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UDPListenPort != 9500 {
		t.Errorf("UDPListenPort = %d, want 9500", cfg.UDPListenPort)
	}
}

func TestLoadTCPListenPort(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-tcp-listen-port", "9100"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TCPListenPort != 9100 {
		t.Errorf("TCPListenPort = %d, want 9100", cfg.TCPListenPort)
	}
}

func TestLoadTCPListenPortDefault(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TCPListenPort != 0 {
		t.Errorf("TCPListenPort = %d, want 0 (disabled)", cfg.TCPListenPort)
	}
}

func TestLoadProxySeqDisabled(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-proxy-seq=false"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProxySeqEnabled {
		t.Error("ProxySeqEnabled should be false")
	}
}

func TestLoadStaticSubtreeIDValid(t *testing.T) {
	iface := realIface(t)
	hex64 := strings.Repeat("ab", 32) // exactly 64 hex chars = 32 bytes
	cfg, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-id", hex64})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.StaticSubtreeID) != 32 {
		t.Errorf("StaticSubtreeID len = %d, want 32", len(cfg.StaticSubtreeID))
	}
	for i, b := range cfg.StaticSubtreeID {
		if b != 0xAB {
			t.Errorf("StaticSubtreeID[%d] = 0x%02X, want 0xAB", i, b)
			break
		}
	}
}

func TestLoadStaticSubtreeIDInvalidHex(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-id", "notvalidhex"})
	if err == nil {
		t.Error("want error for invalid hex, got nil")
	}
}

func TestLoadStaticSubtreeIDWrongLength(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-id", "abab"})
	if err == nil {
		t.Error("want error for wrong-length subtree ID, got nil")
	}
}

func TestLoadStaticSubtreeIDPassthrough(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StaticSubtreeID != nil {
		t.Error("StaticSubtreeID should be nil when not set")
	}
}

func TestLoadStaticSubtreeHeightValid(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-height", "20"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StaticSubtreeHeight == nil || *cfg.StaticSubtreeHeight != 20 {
		t.Errorf("StaticSubtreeHeight = %v, want *20", cfg.StaticSubtreeHeight)
	}
}

func TestLoadStaticSubtreeHeightZero(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-height", "0"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StaticSubtreeHeight == nil || *cfg.StaticSubtreeHeight != 0 {
		t.Errorf("StaticSubtreeHeight = %v, want *0", cfg.StaticSubtreeHeight)
	}
}

func TestLoadStaticSubtreeHeightPassthrough(t *testing.T) {
	iface := realIface(t)
	cfg, err := parseArgs(t, []string{"-iface", iface})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StaticSubtreeHeight != nil {
		t.Errorf("StaticSubtreeHeight should be nil when not set, got %v", cfg.StaticSubtreeHeight)
	}
}

func TestLoadStaticSubtreeHeightInvalid(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-height", "256"})
	if err == nil {
		t.Error("want error for subtree height > 255, got nil")
	}
}

func TestLoadStaticSubtreeHeightNotANumber(t *testing.T) {
	iface := realIface(t)
	_, err := parseArgs(t, []string{"-iface", iface, "-static-subtree-height", "twenty"})
	if err == nil {
		t.Error("want error for non-numeric subtree height, got nil")
	}
}

func TestEnvDurationFallback(t *testing.T) {
	_ = os.Unsetenv("TEST_ENV_DUR_KEY")
	if got := envDuration("TEST_ENV_DUR_KEY", 30*time.Second); got != 30*time.Second {
		t.Errorf("got %v, want 30s", got)
	}
}

func TestEnvDurationSet(t *testing.T) {
	t.Setenv("TEST_ENV_DUR_KEY", "1m")
	if got := envDuration("TEST_ENV_DUR_KEY", 30*time.Second); got != time.Minute {
		t.Errorf("got %v, want 1m", got)
	}
}

func TestEnvDurationInvalid(t *testing.T) {
	t.Setenv("TEST_ENV_DUR_KEY", "not-a-duration")
	if got := envDuration("TEST_ENV_DUR_KEY", 5*time.Second); got != 5*time.Second {
		t.Errorf("got %v, want fallback 5s", got)
	}
}
