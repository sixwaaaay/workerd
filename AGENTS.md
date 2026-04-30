# AGENTS.md — AI Coding Agent Instructions

## Project Overview

**workerd** is a Go user-space process manager (similar to Supervisor / Docker Compose).
Single binary: `workerd daemon` starts the background daemon; other subcommands
(`start`, `stop`, `status`, `logs`, `init`, etc.) communicate via Unix domain socket
HTTP API.

## Tech Stack

- **Language:** Go 1.21+
- **CLI:** `github.com/spf13/cobra`
- **TOML parsing:** `github.com/BurntSushi/toml`
- **JSON Schema:** `github.com/invopop/jsonschema`
- **Log rotation:** `gopkg.in/natefinch/lumberjack.v2`
- **IPC:** HTTP over Unix domain socket (no gRPC)

## Build & Run

```bash
go build -o bin/workerd ./cmd/workerd/
./bin/workerd daemon --foreground --socket /tmp/test.sock --config /tmp/test-cfg
```

## Code Conventions

### Style
- Follow standard Go conventions (`gofmt`, `golint`)
- Use descriptive variable names, avoid single-letter except for receivers (`m *Manager`)
- Prefer `fmt.Errorf("context: %w", err)` for error wrapping
- Use `sync.RWMutex` for read-heavy concurrent access

### Project Layout

```
cmd/workerd/main.go          # CLI entry: cobra commands, daemonize logic
internal/
  config/                    # Config types, TOML parser, JSON Schema generator
    config.go                # ServiceConfig struct + LoadServices() + Validate()
    toml.go                  # TOML decoder adapter
    schema.go                # GenerateSchema() → JSON bytes
  daemon/
    server.go                # HTTP server on Unix socket, signal handling
  process/
    manager.go               # ProcessManager: start/stop/restart, state machine, restart backoff
    health.go                # Health check goroutines (HTTP/TCP/exec)
  logger/
    logger.go                # Log collector, rotation (lumberjack), reader
  client/
    client.go                # HTTP client for CLI → daemon communication
```

### Key Design Decisions

1. **No gRPC** — HTTP over Unix socket is simpler, debuggable with `curl --unix-socket`
2. **One binary** — daemon and CLI share the same binary; `daemon` subcommand forks
3. **Setpgid** — each managed process gets its own process group for clean signal delivery
4. **Health checks run in goroutines** — one per service, canceled on stop
5. **Restart backoff** — `time.AfterFunc` schedules delayed restart after process exit
6. **Log streaming** — newline-delimited JSON (`application/x-ndjson`) for follow mode
7. **No cgroups, no user namespaces** — this is a simple supervisor, not a container runtime

### State Machine

```
stopped → starting → running → running (healthy)
running → stopping → stopped
running → restarting → starting (after backoff)
restarting → failed (if max_retries exceeded)
config error → error
```

### API Endpoints (daemon)

All served on Unix socket:

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/start` | Start a service `{"name":"..."}` |
| POST | `/v1/stop` | Stop a service |
| POST | `/v1/restart` | Restart a service |
| POST | `/v1/add` | Add a service `{"config_path":"..."}` |
| POST | `/v1/remove` | Remove a service `{"name":"..."}` |
| POST | `/v1/reload` | Reload all configs |
| POST | `/v1/shutdown` | Graceful daemon shutdown |
| GET | `/v1/status?name=X` | Service status |
| GET | `/v1/list` | List all services |
| GET | `/v1/logs?name=X&stream=stdout&n=50&follow=true` | Read/follow logs |
| GET | `/v1/schema` | Get JSON Schema |

## Testing

```bash
# Full test suite
./test/run_tests.sh

# Manual smoke test
make smoke-test
```

Test service is a Python HTTP server (`test/test_server.py`) that responds to health checks.

## Common Tasks

### Adding a new CLI command
1. Add the function in `cmd/workerd/main.go` following the pattern of existing commands
2. Register it in `main()` with `rootCmd.AddCommand(xxxCmd())`
3. If it needs daemon interaction, add the method to `internal/client/client.go`
4. If it needs a new API endpoint, add the handler in `internal/daemon/server.go`

### Adding a new config field
1. Add field to the appropriate struct in `internal/config/config.go`
2. Add TOML and jsonschema struct tags
3. Update `Validate()` if needed
4. Regenerate schema: `./bin/workerd schema > schemas/workerd.schema.json`

### Adding a new health check type
1. Add the type constant in `internal/config/config.go`
2. Add the check implementation in `internal/process/health.go`
3. Register in `runHealthCheck()` switch

## Commit Convention

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add shutdown command to gracefully stop daemon
fix: resolve race condition in process state transition
docs: add AGENTS.md for AI-assisted development
refactor: extract health check into separate goroutines
test: add integration tests for restart backoff
chore: update go dependencies
```

Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `chore`, `ci`, `build`
