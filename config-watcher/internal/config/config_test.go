package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")
	content := `
watch:
  - path: /mnt/efs/config/service.conf
    interval: 5s
    debounce: 2s
    cooldown: 10s
    jitter:
      min: 1s
      max: 2s
    detector: mtime_size_sha256
    action:
      type: signal
      pidFile: /run/pids/service.pid
      signal: SIGHUP
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if len(cfg.Watch) != 1 {
		t.Fatalf("unexpected watch count: %d", len(cfg.Watch))
	}
}

func TestValidateInvalidAction(t *testing.T) {
	cfg := Config{
		Watch: []WatchConfig{
			{
				Path:     "/tmp/file",
				Interval: Duration{},
				Detector: DefaultDetector,
				Action: ActionConfig{
					Type: "http",
				},
			},
		},
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestValidateSignalWithProcessName(t *testing.T) {
	cfg := Config{
		Watch: []WatchConfig{
			{
				Path:     "/tmp/file",
				Interval: Duration{},
				Detector: DefaultDetector,
				Action: ActionConfig{
					Type:        "signal",
					ProcessName: "offline-handler",
					Signal:      "SIGTERM",
				},
			},
		},
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidateKubernetesRestartAction(t *testing.T) {
	cfg := Config{
		Watch: []WatchConfig{
			{
				Path:     "/tmp/file",
				Interval: Duration{},
				Detector: DefaultDetector,
				Action: ActionConfig{
					Type: "kubernetes_restart",
					Kubernetes: KubernetesAction{
						WorkloadType: "rollout",
						WorkloadName: "offline-handler",
						Namespace:    "powermanage",
					},
				},
			},
		},
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}
