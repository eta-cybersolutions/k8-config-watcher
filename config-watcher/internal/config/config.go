package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultInterval = 10 * time.Second
	DefaultDetector = "mtime_size_sha256"
)

type Config struct {
	Log   LogConfig     `yaml:"log" json:"log"`
	Watch []WatchConfig `yaml:"watch" json:"watch"`
}

type LogConfig struct {
	Level string `yaml:"level" json:"level"`
}

type WatchConfig struct {
	Path        string       `yaml:"path" json:"path"`
	Interval    Duration     `yaml:"interval" json:"interval"`
	Debounce    Duration     `yaml:"debounce" json:"debounce"`
	Cooldown    Duration     `yaml:"cooldown" json:"cooldown"`
	WaitForFile bool         `yaml:"waitForFile" json:"waitForFile"`
	Jitter      JitterConfig `yaml:"jitter" json:"jitter"`
	Detector    string       `yaml:"detector" json:"detector"`
	Action      ActionConfig `yaml:"action" json:"action"`
}

type JitterConfig struct {
	Min Duration `yaml:"min" json:"min"`
	Max Duration `yaml:"max" json:"max"`
}

type ActionConfig struct {
	Type        string           `yaml:"type" json:"type"`
	PIDFile     string           `yaml:"pidFile" json:"pidFile"`
	ProcessName string           `yaml:"processName" json:"processName"`
	Signal      string           `yaml:"signal" json:"signal"`
	Timeout     Duration         `yaml:"timeout" json:"timeout"`
	Command     []string         `yaml:"command" json:"command"`
	Kubernetes  KubernetesAction `yaml:"kubernetes" json:"kubernetes"`
}

type KubernetesAction struct {
	WorkloadType string `yaml:"workloadType" json:"workloadType"`
	WorkloadName string `yaml:"workloadName" json:"workloadName"`
	Namespace    string `yaml:"namespace" json:"namespace"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err == nil {
		return d.parse(raw)
	}

	var numeric int64
	if err := node.Decode(&numeric); err == nil {
		d.Duration = time.Duration(numeric)
		return nil
	}

	return fmt.Errorf("invalid duration value: %q", node.Value)
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err == nil {
		return d.parse(raw)
	}

	var numeric int64
	if err := json.Unmarshal(data, &numeric); err == nil {
		d.Duration = time.Duration(numeric)
		return nil
	}

	return fmt.Errorf("invalid duration value: %s", string(data))
}

func (d Duration) MarshalYAML() (interface{}, error) {
	return d.String(), nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) parse(raw string) error {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		err = json.Unmarshal(b, &cfg)
	default:
		err = yaml.Unmarshal(b, &cfg)
	}
	if err != nil {
		return Config{}, err
	}

	applyDefaults(&cfg)
	applyEnvOverrides(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}

	for i := range cfg.Watch {
		w := &cfg.Watch[i]
		if w.Interval.Duration == 0 {
			w.Interval.Duration = DefaultInterval
		}
		if w.Detector == "" {
			w.Detector = DefaultDetector
		}
		if w.Action.Timeout.Duration == 0 {
			w.Action.Timeout.Duration = 3 * time.Second
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("EFS_WATCHER_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
}

func (c Config) Validate() error {
	if len(c.Watch) == 0 {
		return errors.New("at least one watch entry is required")
	}

	for i, w := range c.Watch {
		if w.Path == "" {
			return fmt.Errorf("watch[%d]: path is required", i)
		}
		if w.Interval.Duration <= 0 {
			return fmt.Errorf("watch[%d]: interval must be > 0", i)
		}
		if w.Debounce.Duration < 0 {
			return fmt.Errorf("watch[%d]: debounce must be >= 0", i)
		}
		if w.Cooldown.Duration < 0 {
			return fmt.Errorf("watch[%d]: cooldown must be >= 0", i)
		}
		if w.Jitter.Min.Duration < 0 || w.Jitter.Max.Duration < 0 {
			return fmt.Errorf("watch[%d]: jitter values must be >= 0", i)
		}
		if w.Jitter.Max.Duration < w.Jitter.Min.Duration {
			return fmt.Errorf("watch[%d]: jitter.max must be >= jitter.min", i)
		}
		if w.Detector != DefaultDetector {
			return fmt.Errorf("watch[%d]: detector must be %q", i, DefaultDetector)
		}
		if err := validateAction(i, w.Action); err != nil {
			return err
		}
	}
	return nil
}

func validateAction(i int, a ActionConfig) error {
	switch strings.ToLower(a.Type) {
	case "signal":
		if a.PIDFile == "" && a.ProcessName == "" {
			return fmt.Errorf("watch[%d]: either action.pidFile or action.processName is required for signal action", i)
		}
		if a.Signal == "" {
			return fmt.Errorf("watch[%d]: action.signal is required for signal action", i)
		}
	case "command":
		if len(a.Command) == 0 {
			return fmt.Errorf("watch[%d]: action.command is required for command action", i)
		}
	case "kubernetes_restart":
		if a.Kubernetes.WorkloadType == "" {
			return fmt.Errorf("watch[%d]: action.kubernetes.workloadType is required", i)
		}
		if a.Kubernetes.WorkloadName == "" {
			return fmt.Errorf("watch[%d]: action.kubernetes.workloadName is required", i)
		}
		if a.Kubernetes.Namespace == "" {
			return fmt.Errorf("watch[%d]: action.kubernetes.namespace is required", i)
		}
	default:
		return fmt.Errorf("watch[%d]: unsupported action.type %q", i, a.Type)
	}
	return nil
}
