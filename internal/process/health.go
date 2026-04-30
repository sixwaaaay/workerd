package process

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/sixwaaaay/workerd/internal/config"
)

// startHealthCheck begins periodic health checking for the service.
// Must be called with svc.mu locked.
func (s *service) startHealthCheck(m *Manager) {
	if s.Config.HealthCheck == nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.healthCancel = cancel

	hc := s.Config.HealthCheck
	interval := hc.Interval.Duration()
	timeout := hc.Timeout.Duration()
	maxRetries := hc.Retries

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		consecutiveFailures := 0

		// Do first check after interval
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(ctx, timeout)
				err := runHealthCheck(checkCtx, hc)
				checkCancel()

				if err != nil {
					consecutiveFailures++
					if consecutiveFailures >= maxRetries {
						s.handleUnhealthy(m)
						return
					}
				} else {
					consecutiveFailures = 0
					// Mark as healthy
					s.mu.Lock()
					if s.State == StateRunning {
						s.State = StateHealthy
					}
					s.mu.Unlock()
				}
			}
		}
	}()
}

func runHealthCheck(ctx context.Context, hc *config.HealthCheckConfig) error {
	switch hc.Type {
	case "http":
		return httpHealthCheck(ctx, hc)
	case "tcp":
		return tcpHealthCheck(ctx, hc)
	case "exec":
		return execHealthCheck(ctx, hc)
	default:
		return fmt.Errorf("unknown health check type: %s", hc.Type)
	}
}

func httpHealthCheck(ctx context.Context, hc *config.HealthCheckConfig) error {
	req, err := http.NewRequestWithContext(ctx, hc.HTTPMethod, hc.HTTPURL, nil)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}

	client := &http.Client{
		Timeout: hc.Timeout.Duration(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http check: %w", err)
	}
	defer resp.Body.Close()

	expectStatus := hc.HTTPExpectStatus
	if expectStatus == 0 {
		expectStatus = 200
	}
	if resp.StatusCode != expectStatus {
		return fmt.Errorf("expected status %d, got %d", expectStatus, resp.StatusCode)
	}
	return nil
}

func tcpHealthCheck(ctx context.Context, hc *config.HealthCheckConfig) error {
	addr := fmt.Sprintf("%s:%d", hc.TCPHost, hc.TCPPort)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tcp check: %w", err)
	}
	conn.Close()
	return nil
}

func execHealthCheck(ctx context.Context, hc *config.HealthCheckConfig) error {
	execTimeout := hc.ExecTimeout.Duration()
	if execTimeout == 0 {
		execTimeout = hc.Timeout.Duration()
	}

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", hc.ExecCommand)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec check: %w", err)
	}
	return nil
}
