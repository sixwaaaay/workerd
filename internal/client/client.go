package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/sixwaaaay/workerd/internal/logger"
	"github.com/sixwaaaay/workerd/internal/process"
)

// Client communicates with the workerd daemon over Unix socket.
type Client struct {
	socketPath string
	httpClient *http.Client
}

// NewClient creates a new daemon client.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: &http.Transport{
				Dial: func(_, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

// Start starts a service.
func (c *Client) Start(name string) error {
	return c.post("/v1/start", map[string]string{"name": name})
}

// Stop stops a service.
func (c *Client) Stop(name string) error {
	return c.post("/v1/stop", map[string]string{"name": name})
}

// Restart restarts a service.
func (c *Client) Restart(name string) error {
	return c.post("/v1/restart", map[string]string{"name": name})
}

// Add adds a service from a config file.
func (c *Client) Add(configPath string) error {
	return c.post("/v1/add", map[string]string{"config_path": configPath})
}

// Remove removes a service.
func (c *Client) Remove(name string) error {
	return c.post("/v1/remove", map[string]string{"name": name})
}

// Reload reloads all configs.
func (c *Client) Reload() error {
	return c.post("/v1/reload", map[string]string{})
}

// Shutdown gracefully shuts down the daemon.
func (c *Client) Shutdown() error {
	return c.post("/v1/shutdown", map[string]string{})
}

// Status returns status of a service or all services.
func (c *Client) Status(name string) ([]*process.ServiceStatus, error) {
	url := "/v1/status"
	if name != "" {
		url += "?name=" + name
	}
	return c.getStatusList(url)
}

// List returns all services.
func (c *Client) List() ([]*process.ServiceStatus, error) {
	return c.getStatusList("/v1/list")
}

// Logs returns log lines for a service.
func (c *Client) Logs(name, stream string, n int) ([]logger.LogLine, error) {
	url := fmt.Sprintf("/v1/logs?name=%s&stream=%s&n=%d", name, stream, n)
	resp, err := c.httpClient.Get("http://unix" + url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error: %s", string(body))
	}

	var lines []logger.LogLine
	if err := json.NewDecoder(resp.Body).Decode(&lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// LogsFollow streams log lines in real time.
func (c *Client) LogsFollow(name, stream string, n int, callback func(logger.LogLine)) error {
	url := fmt.Sprintf("/v1/logs?name=%s&stream=%s&n=%d&follow=true", name, stream, n)
	resp, err := c.httpClient.Get("http://unix" + url)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server error: %s", string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry logger.LogLine
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		callback(entry)
	}
	return scanner.Err()
}

// Schema returns the JSON schema.
func (c *Client) Schema() ([]byte, error) {
	resp, err := c.httpClient.Get("http://unix/v1/schema")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Ping checks if the daemon is reachable.
func (c *Client) Ping() error {
	resp, err := c.httpClient.Get("http://unix/v1/list")
	if err != nil {
		return fmt.Errorf("daemon not reachable: %w", err)
	}
	resp.Body.Close()
	return nil
}

func (c *Client) post(path string, body interface{}) error {
	data, _ := json.Marshal(body)
	resp, err := c.httpClient.Post("http://unix"+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("request failed: %w (is the daemon running?)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		var errResp map[string]string
		json.Unmarshal(respBody, &errResp)
		if msg, ok := errResp["error"]; ok {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("server returned status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) getStatusList(url string) ([]*process.ServiceStatus, error) {
	resp, err := c.httpClient.Get("http://unix" + url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	// Try array first
	var list []*process.ServiceStatus
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &list); err == nil {
		return list, nil
	}

	// Try single
	var single process.ServiceStatus
	if err := json.Unmarshal(body, &single); err == nil {
		return []*process.ServiceStatus{&single}, nil
	}

	return nil, fmt.Errorf("unexpected response: %s", string(body))
}
