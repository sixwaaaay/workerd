package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Duration wraps time.Duration to support TOML string unmarshaling like "10s", "1m".
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	if dur < 0 {
		return fmt.Errorf("duration must be non-negative: %q", string(text))
	}
	*d = Duration(dur)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// ServiceConfig represents a single service configuration.
type ServiceConfig struct {
	Name        string             `toml:"name" json:"name" jsonschema:"required,description=Unique service name"`
	Command     string             `toml:"command" json:"command" jsonschema:"required,description=Executable path (absolute recommended)"`
	Description string             `toml:"description" json:"description,omitempty" jsonschema:"description=Human-readable description"`
	Args        []string           `toml:"args" json:"args,omitempty" jsonschema:"description=Command-line arguments"`
	WorkingDir  string             `toml:"working_dir" json:"working_dir,omitempty" jsonschema:"description=Working directory for the process"`
	Enabled     bool               `toml:"enabled" json:"enabled,omitempty" jsonschema:"description=Whether to auto-start on daemon launch"`
	Environment map[string]string  `toml:"environment" json:"environment,omitempty" jsonschema:"description=Environment variables"`
	EnvFile     string             `toml:"env_file" json:"env_file,omitempty" jsonschema:"description=Path to .env file to load"`
	Restart     RestartConfig      `toml:"restart" json:"restart,omitempty"`
	HealthCheck *HealthCheckConfig `toml:"health_check" json:"health_check,omitempty"`
	Stop        StopConfig         `toml:"stop" json:"stop,omitempty"`
	Log         LogConfig          `toml:"log" json:"log,omitempty"`
}

// RestartConfig defines the restart policy for a service.
type RestartConfig struct {
	Policy         string   `toml:"policy" json:"policy,omitempty" jsonschema:"enum=no,enum=always,enum=on-failure,enum=unless-stopped,description=Restart policy"`
	ExitCodes      []int    `toml:"exit_codes" json:"exit_codes,omitempty" jsonschema:"description=Exit codes treated as failure (empty = non-zero)"`
	MaxRetries     int      `toml:"max_retries" json:"max_retries,omitempty" jsonschema:"description=Max retries (0 = unlimited)"`
	RestartWindow  Duration `toml:"restart_window" json:"restart_window,omitempty" jsonschema:"description=Time window for counting retries"`
	Backoff        string   `toml:"backoff" json:"backoff,omitempty" jsonschema:"enum=fixed,enum=exponential,description=Backoff strategy"`
	BackoffInitial Duration `toml:"backoff_initial" json:"backoff_initial,omitempty" jsonschema:"description=Initial backoff duration"`
	BackoffMax     Duration `toml:"backoff_max" json:"backoff_max,omitempty" jsonschema:"description=Maximum backoff duration"`
	BackoffFactor  float64  `toml:"backoff_factor" json:"backoff_factor,omitempty" jsonschema:"description=Backoff multiplier for exponential strategy"`
}

// HealthCheckConfig defines how to check if a service is healthy.
type HealthCheckConfig struct {
	Type             string   `toml:"type" json:"type" jsonschema:"required,enum=http,enum=tcp,enum=exec"`
	HTTPURL          string   `toml:"http_url" json:"http_url,omitempty"`
	HTTPMethod       string   `toml:"http_method" json:"http_method,omitempty"`
	HTTPExpectStatus int      `toml:"http_expect_status" json:"http_expect_status,omitempty"`
	TCPHost          string   `toml:"tcp_host" json:"tcp_host,omitempty"`
	TCPPort          int      `toml:"tcp_port" json:"tcp_port,omitempty"`
	ExecCommand      string   `toml:"exec_command" json:"exec_command,omitempty"`
	ExecTimeout      Duration `toml:"exec_timeout" json:"exec_timeout,omitempty"`
	Interval         Duration `toml:"interval" json:"interval,omitempty"`
	Timeout          Duration `toml:"timeout" json:"timeout,omitempty"`
	Retries          int      `toml:"retries" json:"retries,omitempty" jsonschema:"description=Consecutive failures before marking unhealthy"`
	OnUnhealthy      string   `toml:"on_unhealthy" json:"on_unhealthy,omitempty" jsonschema:"enum=restart,enum=none"`
}

// StopConfig defines how a service should be stopped.
type StopConfig struct {
	Signal  string   `toml:"signal" json:"signal,omitempty" jsonschema:"enum=SIGTERM,enum=SIGINT,enum=SIGKILL,description=Signal to send on stop"`
	Timeout Duration `toml:"timeout" json:"timeout,omitempty" jsonschema:"description=Grace period before SIGKILL"`
}

// LogConfig defines log output settings.
type LogConfig struct {
	StdoutPath string `toml:"stdout_path" json:"stdout_path,omitempty" jsonschema:"description=Custom stdout log path"`
	StderrPath string `toml:"stderr_path" json:"stderr_path,omitempty" jsonschema:"description=Custom stderr log path"`
	MaxSize    string `toml:"max_size" json:"max_size,omitempty" jsonschema:"description=Max log file size before rotation (e.g. 100MB)"`
	MaxFiles   int    `toml:"max_files" json:"max_files,omitempty" jsonschema:"description=Number of rotated log files to keep"`
}

// DefaultServiceConfig returns a config pre-filled with sensible defaults.
func DefaultServiceConfig(name string) *ServiceConfig {
	return &ServiceConfig{
		Name:    name,
		Command: "/usr/bin/" + name,
		Enabled: true,
		Restart: RestartConfig{
			Policy:         "on-failure",
			Backoff:        "exponential",
			BackoffInitial: Duration(time.Second),
			BackoffMax:     Duration(60 * time.Second),
			BackoffFactor:  2.0,
			RestartWindow:  Duration(120 * time.Second),
		},
		Stop: StopConfig{
			Signal:  "SIGTERM",
			Timeout: Duration(10 * time.Second),
		},
		Log: LogConfig{
			MaxSize:  "",
			MaxFiles: 0,
		},
	}
}

// LoadService parses a single TOML config file into a ServiceConfig.
func LoadService(path string) (*ServiceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg ServiceConfig
	if err := tomlUnmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing TOML: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}
	return &cfg, nil
}

// LoadServices loads all .toml config files from a directory.
func LoadServices(dir string) ([]*ServiceConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading config directory %s: %w", dir, err)
	}

	var services []*ServiceConfig
	var errs []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		cfg, err := LoadService(fullPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		services = append(services, cfg)
	}

	if len(errs) > 0 {
		return services, fmt.Errorf("errors loading some configs:\n%s", strings.Join(errs, "\n"))
	}
	return services, nil
}

// Validate checks the service config for correctness.
func (c *ServiceConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.Command == "" {
		return fmt.Errorf("command is required")
	}

	// Validate restart policy
	validPolicies := map[string]bool{"no": true, "always": true, "on-failure": true, "unless-stopped": true}
	if c.Restart.Policy != "" && !validPolicies[c.Restart.Policy] {
		return fmt.Errorf("invalid restart policy: %q (valid: no, always, on-failure, unless-stopped)", c.Restart.Policy)
	}

	// Validate backoff
	validBackoffs := map[string]bool{"fixed": true, "exponential": true}
	if c.Restart.Backoff != "" && !validBackoffs[c.Restart.Backoff] {
		return fmt.Errorf("invalid backoff strategy: %q (valid: fixed, exponential)", c.Restart.Backoff)
	}

	// Validate stop signal
	validSignals := map[string]bool{"SIGTERM": true, "SIGINT": true, "SIGKILL": true}
	if c.Stop.Signal != "" && !validSignals[c.Stop.Signal] {
		return fmt.Errorf("invalid stop signal: %q (valid: SIGTERM, SIGINT, SIGKILL)", c.Stop.Signal)
	}

	// Validate health check
	if c.HealthCheck != nil {
		if err := c.HealthCheck.Validate(); err != nil {
			return fmt.Errorf("health_check: %w", err)
		}
	}

	return nil
}

// Validate checks the health check config for correctness.
func (h *HealthCheckConfig) Validate() error {
	validTypes := map[string]bool{"http": true, "tcp": true, "exec": true}
	if !validTypes[h.Type] {
		return fmt.Errorf("invalid type: %q (valid: http, tcp, exec)", h.Type)
	}

	switch h.Type {
	case "http":
		if h.HTTPURL == "" {
			return fmt.Errorf("http_url is required for HTTP health check")
		}
		if h.HTTPExpectStatus == 0 {
			h.HTTPExpectStatus = 200
		}
		if h.HTTPMethod == "" {
			h.HTTPMethod = "GET"
		}
	case "tcp":
		if h.TCPHost == "" || h.TCPPort == 0 {
			return fmt.Errorf("tcp_host and tcp_port are required for TCP health check")
		}
	case "exec":
		if h.ExecCommand == "" {
			return fmt.Errorf("exec_command is required for exec health check")
		}
	}

	if h.Interval.Duration() == 0 {
		h.Interval = Duration(10 * time.Second)
	}
	if h.Timeout.Duration() == 0 {
		h.Timeout = Duration(5 * time.Second)
	}
	if h.Retries == 0 {
		h.Retries = 3
	}
	if h.OnUnhealthy == "" {
		h.OnUnhealthy = "restart"
	}

	return nil
}

// tomlUnmarshal is a thin wrapper around the BurntSushi/toml decoder.
func tomlUnmarshal(data []byte, v interface{}) error {
	// We use BurntSushi/toml directly in the real implementation.
	// This file is structured so we can swap implementations easily.
	return unmarshalTOML(data, v)
}
