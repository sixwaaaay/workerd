# workerd

A lightweight user-space process manager in Go — like Supervisor meets Docker Compose.

Manages long-running services defined in TOML config files, with health checks,
restart policies, log collection & rotation, and a `systemctl`-like CLI.

## Features

- **Single binary** — `workerd daemon` runs the daemon; all other subcommands control it
- **TOML configs** — one file per service, similar to docker-compose
- **JSON Schema** — validate configs and enable IDE autocompletion (Even Better TOML / YAML LSP)
- **Health checks** — HTTP, TCP, or exec-based; auto-restart on failure
- **Restart policies** — `no`, `always`, `on-failure`, `unless-stopped` with exponential backoff
- **Log collection** — stdout & stderr captured per service with automatic rotation
- **Graceful shutdown** — `SIGTERM` → timeout → `SIGKILL`
- **Lightweight** — Unix domain socket IPC, no gRPC, no systemd dependency

## Installation

```bash
go install github.com/sixwaaaay/workerd/cmd/workerd@latest
```

Or build from source:

```bash
git clone https://github.com/sixwaaaay/workerd.git
cd workerd
make build
```

## Quick Start

### 1. Start the daemon

```bash
# Run in foreground (for testing)
workerd daemon --foreground

# Run in background (default)
workerd daemon
```

### 2. Create a service config

```bash
# Generate a template
workerd init myapp

# Edit the config
vim ~/.config/workerd/services/myapp.toml
```

Example config (`myapp.toml`):

```toml
#:schema https://raw.githubusercontent.com/sixwaaaay/workerd/refs/heads/main/schemas/workerd.schema.json

name = "myapp"
command = "/usr/bin/python3"
args = ["-m", "http.server", "8080"]
working_dir = "/tmp"
enabled = true

[environment]
PORT = "8080"

[restart]
policy = "on-failure"
max_retries = 3
backoff = "exponential"
backoff_initial = "1s"
backoff_max = "60s"

[health_check]
type = "http"
http_url = "http://localhost:8080/"
interval = "10s"
timeout = "5s"
retries = 3
on_unhealthy = "restart"

[stop]
signal = "SIGTERM"
timeout = "10s"

[log]
max_size = "100MB"
max_files = 5
```

### 3. Manage services

```bash
# Add and start
workerd add ~/.config/workerd/services/myapp.toml
workerd start myapp

# View status
workerd status           # all services
workerd status myapp     # single service
workerd ps               # table view

# View logs
workerd logs myapp               # last 50 lines
workerd logs myapp -f            # follow (tail -f)
workerd logs myapp -n 100        # last 100 lines
workerd logs myapp --stderr      # stderr stream

# Stop / restart / remove
workerd stop myapp
workerd restart myapp
workerd remove myapp

# Reload configs (adds new, removes deleted)
workerd reload

# Stop the daemon
workerd shutdown
```

## Configuration Reference

### `[restart]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `policy` | string | `"on-failure"` | `no`, `always`, `on-failure`, `unless-stopped` |
| `exit_codes` | []int | `[]` | Exit codes treated as failure (empty = non-zero) |
| `max_retries` | int | `0` | Max retries (0 = unlimited) |
| `restart_window` | duration | `"120s"` | Reset counter after this window |
| `backoff` | string | `"exponential"` | `fixed` or `exponential` |
| `backoff_initial` | duration | `"1s"` | Initial backoff |
| `backoff_max` | duration | `"60s"` | Maximum backoff |
| `backoff_factor` | float | `2.0` | Multiplier for exponential |

### `[health_check]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | — | `http`, `tcp`, or `exec` |
| `http_url` | string | — | URL for HTTP check |
| `http_method` | string | `"GET"` | HTTP method |
| `http_expect_status` | int | `200` | Expected status code |
| `tcp_host` | string | — | Host for TCP check |
| `tcp_port` | int | — | Port for TCP check |
| `exec_command` | string | — | Shell command for exec check |
| `interval` | duration | `"10s"` | Check interval |
| `timeout` | duration | `"5s"` | Check timeout |
| `retries` | int | `3` | Consecutive failures before unhealthy |
| `on_unhealthy` | string | `"restart"` | Action: `restart` or `none` |

### `[stop]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `signal` | string | `"SIGTERM"` | Stop signal |
| `timeout` | duration | `"10s"` | Wait before SIGKILL |

### `[log]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `stdout_path` | string | auto | Custom stdout log path |
| `stderr_path` | string | auto | Custom stderr log path |
| `max_size` | string | `"100MB"` | Max size before rotation |
| `max_files` | int | `5` | Rotated files to keep |

## Directory Layout

```
~/.config/workerd/          # User mode (or /etc/workerd/ for root)
├── daemon.log              # Daemon output
├── workerd.pid             # PID file
├── workerd.sock            # Unix socket
├── services/               # Service TOML configs
│   ├── myapp.toml
│   └── redis.toml
└── logs/                   # Service log files
    ├── myapp/
    │   ├── stdout.log
    │   └── stderr.log
    └── redis/
        ├── stdout.log
        └── stderr.log
```

## CLI Reference

```
workerd daemon [--foreground]     Start the daemon
workerd init <name>               Generate a service config template
workerd add <config-file>         Load a service config
workerd remove <name>             Remove a service
workerd start <name>              Start a service
workerd stop <name>               Stop a service
workerd restart <name>            Restart a service
workerd status [name]             Show service status
workerd ps                        List all services
workerd logs <name> [-f] [-n N] [--stderr]  View logs
workerd reload                    Reload all configs
workerd shutdown                  Gracefully stop the daemon
workerd schema                    Output JSON Schema
workerd version                   Print version
```

### Global Flags

```
--socket string    Unix socket path
--config string    Config directory
```

## IDE Integration

The project includes a JSON Schema for TOML config files. Add this line to the top of
each service config to enable autocompletion in VS Code (requires
[Even Better TOML](https://marketplace.visualstudio.com/items?itemName=tamasfe.even-better-toml)):

```toml
#:schema https://raw.githubusercontent.com/sixwaaaay/workerd/refs/heads/main/schemas/workerd.schema.json
```

Generate the schema:

```bash
workerd schema > schemas/workerd.schema.json
```

## Development

```bash
# Build
make build

# Run tests
make test

# Manual smoke test
make smoke-test
```

## License

MIT
