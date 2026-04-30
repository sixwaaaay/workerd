package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/sixwaaaay/workerd/internal/config"
	"github.com/sixwaaaay/workerd/internal/logger"
	"github.com/sixwaaaay/workerd/internal/process"
)

// Server is the workerd daemon server.
type Server struct {
	socketPath string
	configDir  string
	logDir     string
	pidFile    string
	manager    *process.Manager
	httpServer *http.Server
}

// NewServer creates a new daemon server.
func NewServer(socketPath, configDir string) *Server {
	logDir := filepath.Join(configDir, "logs")
	pidFile := filepath.Join(configDir, "workerd.pid")

	return &Server{
		socketPath: socketPath,
		configDir:  configDir,
		logDir:     logDir,
		pidFile:    pidFile,
		manager:    process.NewManager(configDir, logDir),
	}
}

// Run starts the daemon server. Blocks until shutdown.
func (s *Server) Run() error {
	// Ensure config directories exist
	os.MkdirAll(filepath.Join(s.configDir, "services"), 0755)
	os.MkdirAll(s.logDir, 0755)

	// Remove old socket
	os.Remove(s.socketPath)

	// Create Unix listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("creating unix socket: %w", err)
	}
	defer os.Remove(s.socketPath)

	// Set socket permissions
	os.Chmod(s.socketPath, 0666)

	// Write PID file
	os.WriteFile(s.pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
	defer os.Remove(s.pidFile)

	// Setup HTTP mux
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/start", s.handleStart)
	mux.HandleFunc("/v1/stop", s.handleStop)
	mux.HandleFunc("/v1/restart", s.handleRestart)
	mux.HandleFunc("/v1/add", s.handleAdd)
	mux.HandleFunc("/v1/remove", s.handleRemove)
	mux.HandleFunc("/v1/reload", s.handleReload)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/list", s.handleList)
	mux.HandleFunc("/v1/logs", s.handleLogs)
	mux.HandleFunc("/v1/schema", s.handleSchema)
	mux.HandleFunc("/v1/shutdown", s.handleShutdown)

	s.httpServer = &http.Server{
		Handler: mux,
	}

	// Load services
	fmt.Printf("Loading services from %s\n", filepath.Join(s.configDir, "services"))
	loaded, errs := s.manager.LoadServices()
	if errs != nil {
		fmt.Printf("Warning: %v\n", errs)
	}
	fmt.Printf("Loaded %d service(s)\n", len(loaded))

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGTERM, syscall.SIGINT:
				fmt.Printf("\nReceived %v, shutting down...\n", sig)
				s.manager.StopAll()
				s.httpServer.Close()
				return
			case syscall.SIGHUP:
				fmt.Println("Received SIGHUP, reloading configs...")
				s.manager.Reload()
			}
		}
	}()

	fmt.Printf("Daemon listening on %s\n", s.socketPath)

	if err := s.httpServer.Serve(listener); err != http.ErrServerClosed {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the daemon.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.manager.StopAll()
	s.httpServer.Shutdown(ctx)
}

// ---- HTTP Handlers ----

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.manager.StartService(req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started", "name": req.Name})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.manager.StopService(req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "name": req.Name})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.manager.RestartService(req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarted", "name": req.Name})
}

func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		ConfigPath string `json:"config_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ConfigPath == "" {
		writeError(w, http.StatusBadRequest, "config_path is required")
		return
	}
	if err := s.manager.AddService(req.ConfigPath); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.manager.RemoveService(req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "name": req.Name})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err := s.manager.Reload(); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "reloaded",
			"warning": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := r.URL.Query().Get("name")
	if name != "" {
		status, err := s.manager.GetStatus(name)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	// List all
	services := s.manager.ListServices()
	writeJSON(w, http.StatusOK, services)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	services := s.manager.ListServices()
	writeJSON(w, http.StatusOK, services)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	nStr := r.URL.Query().Get("n")
	n := 50
	if nStr != "" {
		if parsed, err := strconv.Atoi(nStr); err == nil && parsed > 0 {
			n = parsed
		}
	}
	followStr := r.URL.Query().Get("follow")
	follow := followStr == "true" || followStr == "1"

	if follow {
		s.handleLogsFollow(w, r, name, stream, n)
		return
	}

	// Read from file
	logPath, err := s.manager.GetLogPath(name, stream)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	reader := &logger.Reader{}
	lines, err := reader.ReadLastNLines(logPath, n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, lines)
}

func (s *Server) handleLogsFollow(w http.ResponseWriter, r *http.Request, name, stream string, n int) {
	// First send the last n lines
	logPath, err := s.manager.GetLogPath(name, stream)
	if err == nil {
		reader := &logger.Reader{}
		lines, _ := reader.ReadLastNLines(logPath, n)
		for _, line := range lines {
			data, _ := json.Marshal(line)
			fmt.Fprintf(w, "%s\n", data)
		}
	}

	// Then follow
	ch, unsub, err := s.manager.SubscribeLogs(name, stream)
	if err != nil {
		if logPath == "" {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		return
	}
	defer unsub()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(line)
			fmt.Fprintf(w, "%s\n", data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})

	// Trigger graceful shutdown in a goroutine so the response can be sent
	go func() {
		s.manager.StopAll()
		s.httpServer.Close()
	}()
}

func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := config.GenerateSchema()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(schema)
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
