package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesUpstreamDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Upstream.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.Upstream.TimeoutSeconds)
	}
	if cfg.Upstream.UserAgent != DefaultUpstreamUserAgent {
		t.Fatalf("user agent = %q", cfg.Upstream.UserAgent)
	}
}

func TestUpstreamConfigWithDefaultsKeepsCustomUserAgent(t *testing.T) {
	cfg := UpstreamConfig{
		TimeoutSeconds: 0,
		UserAgent:      "custom-agent",
	}.WithDefaults()
	if cfg.TimeoutSeconds != DefaultUpstreamTimeoutSeconds {
		t.Fatalf("timeout seconds = %d", cfg.TimeoutSeconds)
	}
	if cfg.UserAgent != "custom-agent" {
		t.Fatalf("user agent = %q", cfg.UserAgent)
	}
}

func TestLoadAppliesSchedulerTaskTimeoutDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Scheduler.BalanceTimeoutSeconds != DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("balance timeout seconds = %d", cfg.Scheduler.BalanceTimeoutSeconds)
	}
	if cfg.Scheduler.RateTimeoutSeconds != DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("rate timeout seconds = %d", cfg.Scheduler.RateTimeoutSeconds)
	}
}

func TestLoadNormalizesNonPositiveSchedulerTaskTimeouts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("scheduler:\n  balanceTimeoutSeconds: 0\n  rateTimeoutSeconds: -1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.Scheduler.BalanceTimeoutSeconds != DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("balance timeout seconds = %d", cfg.Scheduler.BalanceTimeoutSeconds)
	}
	if cfg.Scheduler.RateTimeoutSeconds != DefaultSchedulerTaskTimeoutSeconds {
		t.Fatalf("rate timeout seconds = %d", cfg.Scheduler.RateTimeoutSeconds)
	}
}

func TestSchedulerConfigWithDefaultsPreservesPositiveTaskTimeouts(t *testing.T) {
	cfg := SchedulerConfig{
		BalanceTimeoutSeconds: 120,
		RateTimeoutSeconds:    240,
	}.WithDefaults()
	if cfg.BalanceTimeoutSeconds != 120 || cfg.RateTimeoutSeconds != 240 {
		t.Fatalf("scheduler config = %#v", cfg)
	}
}
