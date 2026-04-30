package process

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sixwaaaay/workerd/internal/config"
	"github.com/sixwaaaay/workerd/internal/logger"
)

// ServiceState represents the current state of a managed service.
type ServiceState string

const (
	StateStopped    ServiceState = "stopped"
	StateStarting   ServiceState = "starting"
	StateRunning    ServiceState = "running"
	StateHealthy    ServiceState = "running (healthy)"
	StateStopping   ServiceState = "stopping"
	StateRestarting ServiceState = "restarting"
	StateFailed     ServiceState = "failed"
	StateError      ServiceState = "error"
)

// ServiceStatus is the public-facing status of a service.
type ServiceStatus struct {
	Name         string `json:"name"`
	State        string `json:"state"`
	PID          int    `json:"pid"`
	Uptime       string `json:"uptime,omitempty"`
	RestartCount int    `json:"restart_count"`
	ExitCode     int    `json:"exit_code,omitempty"`
	Error        string `json:"error,omitempty"`
	Description  string `json:"description,omitempty"`
}

// service wraps a single managed process.
type service struct {
	mu           sync.Mutex
	Config       *config.ServiceConfig
	State        ServiceState
	cmd          *exec.Cmd
	cancel       context.CancelFunc
	pid          int
	startTime    time.Time
	restartCount int
	lastRestart  time.Time
	manualStop   bool
	exitCode     int
	errorMsg     string

	stdoutCollector *logger.Collector
	stderrCollector *logger.Collector

	healthCancel context.CancelFunc
}

// Manager orchestrates multiple managed services.
type Manager struct {
	mu        sync.RWMutex
	services  map[string]*service
	configDir string
	logDir    string
}

// NewManager creates a new process manager.
func NewManager(configDir, logDir string) *Manager {
	return &Manager{
		services:  make(map[string]*service),
		configDir: configDir,
		logDir:    logDir,
	}
}

// LoadServices loads all service configs from the config directory into the manager.
// Returns errors for invalid configs but doesn't stop loading valid ones.
func (m *Manager) LoadServices() ([]string, error) {
	serviceDir := filepath.Join(m.configDir, "services")
	cfgs, err := config.LoadServices(serviceDir)

	m.mu.Lock()
	defer m.mu.Unlock()

	var loaded []string
	for _, cfg := range cfgs {
		if _, exists := m.services[cfg.Name]; !exists {
			svc := &service{
				Config: cfg,
				State:  StateStopped,
			}
			// If config is invalid, mark as error
			if cfgErr := cfg.Validate(); cfgErr != nil {
				svc.State = StateError
				svc.errorMsg = cfgErr.Error()
			}
			m.services[cfg.Name] = svc
			loaded = append(loaded, cfg.Name)
		}
	}

	// Auto-start enabled services
	for _, name := range loaded {
		svc := m.services[name]
		if svc.Config.Enabled && svc.State == StateStopped {
			go m.startService(svc)
		}
	}

	return loaded, err
}

// AddService adds a single service from a config file path.
func (m *Manager) AddService(configPath string) error {
	cfg, err := config.LoadService(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.services[cfg.Name]; exists {
		return fmt.Errorf("service %q already exists", cfg.Name)
	}

	svc := &service{
		Config: cfg,
		State:  StateStopped,
	}
	m.services[cfg.Name] = svc

	if cfg.Enabled {
		go m.startService(svc)
	}
	return nil
}

// RemoveService removes a service. Stops it first if running.
func (m *Manager) RemoveService(name string) error {
	m.mu.Lock()
	_, ok := m.services[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("service %q not found", name)
	}
	m.mu.Unlock()

	// Stop if running
	m.stopService(name)

	m.mu.Lock()
	delete(m.services, name)
	m.mu.Unlock()
	return nil
}

// StartService starts a service by name.
func (m *Manager) StartService(name string) error {
	m.mu.RLock()
	svc, ok := m.services[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("service %q not found", name)
	}

	svc.mu.Lock()
	if svc.State == StateRunning || svc.State == StateHealthy || svc.State == StateStarting {
		svc.mu.Unlock()
		return fmt.Errorf("service %q is already running", name)
	}
	svc.manualStop = false
	svc.restartCount = 0
	svc.exitCode = 0
	svc.errorMsg = ""
	svc.mu.Unlock()

	return m.startService(svc)
}

// StopService stops a service by name.
func (m *Manager) StopService(name string) error {
	return m.stopService(name)
}

// RestartService restarts a service by name.
func (m *Manager) RestartService(name string) error {
	if err := m.stopService(name); err != nil {
		// It's ok if it's not running
		if err.Error() != fmt.Sprintf("service %q is not running", name) {
			return err
		}
	}
	return m.StartService(name)
}

// GetStatus returns the status of a single service or all services.
func (m *Manager) GetStatus(name string) (*ServiceStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if name != "" {
		svc, ok := m.services[name]
		if !ok {
			return nil, fmt.Errorf("service %q not found", name)
		}
		return svc.status(), nil
	}
	return nil, nil // caller should use ListServices
}

// ListServices returns the status of all services.
func (m *Manager) ListServices() []*ServiceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*ServiceStatus
	for _, svc := range m.services {
		result = append(result, svc.status())
	}
	return result
}

// SubscribeLogs subscribes to live log output for a service.
func (m *Manager) SubscribeLogs(name, stream string) (<-chan logger.LogLine, func(), error) {
	m.mu.RLock()
	svc, ok := m.services[name]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, fmt.Errorf("service %q not found", name)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()

	var collector *logger.Collector
	if stream == "stderr" {
		collector = svc.stderrCollector
	} else {
		collector = svc.stdoutCollector
	}

	if collector == nil {
		return nil, nil, fmt.Errorf("service %q has no log collector", name)
	}

	ch := collector.Subscribe(100)
	unsub := func() {
		collector.Unsubscribe(ch)
	}
	return ch, unsub, nil
}

// GetLogPath returns the log file path for a service and stream.
func (m *Manager) GetLogPath(name, stream string) (string, error) {
	m.mu.RLock()
	svc, ok := m.services[name]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("service %q not found", name)
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()

	if stream == "stderr" && svc.stderrCollector != nil {
		return svc.stderrCollector.Path(), nil
	}
	if svc.stdoutCollector != nil {
		return svc.stdoutCollector.Path(), nil
	}
	return "", fmt.Errorf("no log file for service %q", name)
}

// StopAll stops all running services. Used during daemon shutdown.
func (m *Manager) StopAll() {
	m.mu.RLock()
	names := make([]string, 0, len(m.services))
	for name := range m.services {
		names = append(names, name)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			m.stopService(n)
		}(name)
	}
	wg.Wait()
}

// Reload reloads configs: adds new, removes deleted, updates existing.
func (m *Manager) Reload() error {
	serviceDir := filepath.Join(m.configDir, "services")
	cfgs, err := config.LoadServices(serviceDir)

	m.mu.Lock()
	// Build set of config names
	cfgNames := make(map[string]*config.ServiceConfig)
	if err == nil {
		for _, cfg := range cfgs {
			cfgNames[cfg.Name] = cfg
		}
	}

	// Remove services whose configs were deleted
	for name := range m.services {
		if _, exists := cfgNames[name]; !exists {
			svc := m.services[name]
			m.mu.Unlock()
			m.stopService(name)
			m.mu.Lock()
			delete(m.services, name)
			_ = svc
		}
	}

	// Add new services and update existing
	for name, cfg := range cfgNames {
		if existing, ok := m.services[name]; ok {
			existing.mu.Lock()
			existing.Config = cfg
			existing.mu.Unlock()
		} else {
			svc := &service{
				Config: cfg,
				State:  StateStopped,
			}
			if cfgErr := cfg.Validate(); cfgErr != nil {
				svc.State = StateError
				svc.errorMsg = cfgErr.Error()
			}
			m.services[name] = svc
		}
	}
	m.mu.Unlock()

	// Auto-start newly added enabled services
	for _, cfg := range cfgs {
		m.mu.RLock()
		svc := m.services[cfg.Name]
		m.mu.RUnlock()
		if svc != nil {
			svc.mu.Lock()
			enabled := svc.Config.Enabled
			state := svc.State
			svc.mu.Unlock()
			if enabled && state == StateStopped {
				go m.startService(svc)
			}
		}
	}

	return err
}

// ---- internal methods ----

func (m *Manager) startService(svc *service) error {
	svc.mu.Lock()
	defer svc.mu.Unlock()

	if svc.State == StateRunning || svc.State == StateHealthy || svc.State == StateStarting {
		return nil
	}

	svc.State = StateStarting

	// Build command
	cmd := exec.Command(svc.Config.Command, svc.Config.Args...)

	// Working directory
	if svc.Config.WorkingDir != "" {
		if _, err := os.Stat(svc.Config.WorkingDir); os.IsNotExist(err) {
			svc.State = StateFailed
			svc.errorMsg = fmt.Sprintf("working directory does not exist: %s", svc.Config.WorkingDir)
			return fmt.Errorf(svc.errorMsg)
		}
		cmd.Dir = svc.Config.WorkingDir
	}

	// Environment
	cmd.Env = m.buildEnv(svc.Config)

	// Process group for clean kill
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Setup pipes
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		svc.State = StateFailed
		svc.errorMsg = fmt.Sprintf("stdout pipe: %v", err)
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		svc.State = StateFailed
		svc.errorMsg = fmt.Sprintf("stderr pipe: %v", err)
		return err
	}

	// Create log collectors
	stdoutPath := svc.Config.Log.StdoutPath
	if stdoutPath == "" {
		stdoutPath = filepath.Join(m.logDir, svc.Config.Name, "stdout.log")
	}
	stderrPath := svc.Config.Log.StderrPath
	if stderrPath == "" {
		stderrPath = filepath.Join(m.logDir, svc.Config.Name, "stderr.log")
	}

	stdoutCol, err := logger.NewCollector(stdoutPath, svc.Config.Log.MaxSize, svc.Config.Log.MaxFiles)
	if err != nil {
		svc.State = StateFailed
		svc.errorMsg = fmt.Sprintf("stdout logger: %v", err)
		return err
	}
	stderrCol, err := logger.NewCollector(stderrPath, svc.Config.Log.MaxSize, svc.Config.Log.MaxFiles)
	if err != nil {
		stdoutCol.Close()
		svc.State = StateFailed
		svc.errorMsg = fmt.Sprintf("stderr logger: %v", err)
		return err
	}

	// Start process
	if err := cmd.Start(); err != nil {
		stdoutCol.Close()
		stderrCol.Close()
		svc.State = StateFailed
		svc.errorMsg = fmt.Sprintf("start failed: %v", err)
		return err
	}

	svc.cmd = cmd
	svc.pid = cmd.Process.Pid
	svc.startTime = time.Now()
	svc.State = StateRunning
	svc.stdoutCollector = stdoutCol
	svc.stderrCollector = stderrCol

	// Start log collectors
	go stdoutCol.Collect(stdoutPipe, "stdout")
	go stderrCol.Collect(stderrPipe, "stderr")

	// Start health checks if configured
	if svc.Config.HealthCheck != nil {
		svc.startHealthCheck(m)
	}

	// Monitor process exit
	go m.monitorProcess(svc)

	return nil
}

func (m *Manager) monitorProcess(svc *service) {
	err := svc.cmd.Wait()

	svc.mu.Lock()
	// Stop health checks
	if svc.healthCancel != nil {
		svc.healthCancel()
		svc.healthCancel = nil
	}

	// Close collectors
	if svc.stdoutCollector != nil {
		svc.stdoutCollector.Close()
	}
	if svc.stderrCollector != nil {
		svc.stderrCollector.Close()
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	svc.exitCode = exitCode

	// If we're in the process of stopping, mark as stopped
	if svc.State == StateStopping {
		svc.State = StateStopped
		svc.mu.Unlock()
		return
	}

	// Check restart policy
	if svc.shouldRestartLocked() {
		svc.State = StateRestarting
		svc.restartCount++
		svc.lastRestart = time.Now()

		backoff := svc.calculateBackoffLocked()
		svc.mu.Unlock()

		// Schedule restart after backoff
		time.AfterFunc(backoff, func() {
			svc.mu.Lock()
			if svc.State == StateRestarting {
				svc.mu.Unlock()
				m.startService(svc)
			} else {
				svc.mu.Unlock()
			}
		})
		return
	}

	// Not restarting
	if svc.exitCode != 0 && svc.State != StateStopped {
		svc.State = StateFailed
	} else {
		svc.State = StateStopped
	}
	svc.mu.Unlock()
}

func (m *Manager) stopService(name string) error {
	m.mu.RLock()
	svc, ok := m.services[name]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("service %q not found", name)
	}

	svc.mu.Lock()
	state := svc.State
	if state == StateStopped || state == StateFailed || state == StateError {
		svc.mu.Unlock()
		return fmt.Errorf("service %q is not running", name)
	}

	svc.manualStop = true
	svc.State = StateStopping

	// Cancel any health check
	if svc.healthCancel != nil {
		svc.healthCancel()
		svc.healthCancel = nil
	}

	cmd := svc.cmd
	pid := svc.pid
	timeout := svc.Config.Stop.Timeout.Duration()
	svc.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		svc.mu.Lock()
		svc.State = StateStopped
		svc.mu.Unlock()
		return nil
	}

	// Send signal
	sig := signalFromString(svc.Config.Stop.Signal)

	// Try to send to process group
	if pgid, err := syscall.Getpgid(pid); err == nil {
		syscall.Kill(-pgid, sig)
	} else {
		cmd.Process.Signal(sig)
	}

	// Wait for process to exit with timeout
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Process exited cleanly
	case <-time.After(timeout):
		// Timeout, force kill
		if pgid, err := syscall.Getpgid(pid); err == nil {
			syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			cmd.Process.Kill()
		}
		<-done
	}

	// Update state
	svc.mu.Lock()
	if svc.stdoutCollector != nil {
		svc.stdoutCollector.Close()
	}
	if svc.stderrCollector != nil {
		svc.stderrCollector.Close()
	}
	svc.State = StateStopped
	svc.mu.Unlock()

	return nil
}

// ---- service helper methods ----

func (s *service) status() *ServiceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := &ServiceStatus{
		Name:         s.Config.Name,
		State:        string(s.State),
		PID:          s.pid,
		RestartCount: s.restartCount,
		ExitCode:     s.exitCode,
		Error:        s.errorMsg,
		Description:  s.Config.Description,
	}

	if !s.startTime.IsZero() && (s.State == StateRunning || s.State == StateHealthy) {
		st.Uptime = formatDuration(time.Since(s.startTime))
	}

	return st
}

func (s *service) shouldRestartLocked() bool {
	if s.manualStop {
		return false
	}

	policy := s.Config.Restart.Policy
	if policy == "" || policy == "no" {
		return false
	}

	if policy == "always" {
		return true
	}

	if policy == "unless-stopped" {
		return !s.manualStop
	}

	// "on-failure"
	if s.exitCode == 0 {
		return false
	}

	// Check specific exit codes
	if len(s.Config.Restart.ExitCodes) > 0 {
		for _, code := range s.Config.Restart.ExitCodes {
			if code == s.exitCode {
				return true
			}
		}
		return false
	}

	// Check max retries
	maxRetries := s.Config.Restart.MaxRetries
	if maxRetries > 0 && s.restartCount >= maxRetries {
		// Check if we should reset the counter based on window
		if s.Config.Restart.RestartWindow.Duration() > 0 {
			if time.Since(s.lastRestart) > s.Config.Restart.RestartWindow.Duration() {
				s.restartCount = 0
			} else {
				return false
			}
		} else {
			return false
		}
	}

	return true
}

func (s *service) calculateBackoffLocked() time.Duration {
	backoff := s.Config.Restart.BackoffInitial.Duration()
	if backoff == 0 {
		backoff = time.Second
	}

	if s.Config.Restart.Backoff == "exponential" {
		factor := s.Config.Restart.BackoffFactor
		if factor < 1 {
			factor = 2.0
		}
		backoff = time.Duration(float64(backoff) * math.Pow(factor, float64(s.restartCount-1)))
	}

	maxBackoff := s.Config.Restart.BackoffMax.Duration()
	if maxBackoff == 0 {
		maxBackoff = 60 * time.Second
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	return backoff
}

func (m *Manager) buildEnv(cfg *config.ServiceConfig) []string {
	env := os.Environ()

	// Load .env file if specified
	if cfg.EnvFile != "" {
		envVars := loadEnvFile(cfg.EnvFile)
		env = append(env, envVars...)
	}

	// Add configured environment variables (override .env and system env)
	for k, v := range cfg.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	return env
}

// handleUnhealthy is called by the health checker when a service becomes unhealthy.
func (s *service) handleUnhealthy(m *Manager) {
	s.mu.Lock()
	action := s.Config.HealthCheck.OnUnhealthy
	s.mu.Unlock()

	if action == "restart" {
		m.RestartService(s.Config.Name)
	}
}

func signalFromString(sig string) syscall.Signal {
	switch sig {
	case "SIGINT":
		return syscall.SIGINT
	case "SIGKILL":
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// loadEnvFile loads environment variables from a .env file.
func loadEnvFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var env []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		env = append(env, line)
	}
	return env
}
