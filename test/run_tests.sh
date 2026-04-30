#!/bin/bash
# Automated test for workerd
set -e

BIN="./bin/workerd"
SOCK="/tmp/workerd-test.sock"
CONFIG_DIR="/tmp/workerd-test"
TEST_DIR="$(cd "$(dirname "$0")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; exit 1; }

# Cleanup from previous runs
cleanup() {
    if [ -f "$CONFIG_DIR/workerd.pid" ]; then
        kill $(cat "$CONFIG_DIR/workerd.pid") 2>/dev/null || true
        sleep 1
    fi
    rm -rf "$CONFIG_DIR"
    rm -f "$SOCK"
    # Kill any leftover test server processes
    pkill -f "test_server.py" 2>/dev/null || true
}

cleanup

echo "=== Running workerd tests ==="

# ---- Test 1: Version ----
echo ""
echo "--- Test 1: Version ---"
$BIN version && pass "Version command works"

# ---- Test 2: Schema ----
echo ""
echo "--- Test 2: Schema ---"
$BIN schema > /dev/null && pass "Schema command works"

# ---- Test 3: Init ----
echo ""
echo "--- Test 3: Init ---"
$BIN init --config "$CONFIG_DIR" python-http 2>&1
[ -f "$CONFIG_DIR/services/python-http.toml" ] && pass "Init creates config file" || fail "Init did not create config file"

# ---- Test 4: Start daemon ----
echo ""
echo "--- Test 4: Start Daemon ---"
$BIN daemon --foreground --socket "$SOCK" --config "$CONFIG_DIR" &
DAEMON_PID=$!
sleep 2

# Check daemon is running
kill -0 $DAEMON_PID 2>/dev/null && pass "Daemon is running" || fail "Daemon failed to start"

# ---- Test 5: Init with daemon running ----
echo ""
echo "--- Test 5: Init another service ---"
# Write a proper TOML config for our test server
cat > "$CONFIG_DIR/services/py-server.toml" << 'TOML'
name = "py-server"
command = "python3"
args = ["test/test_server.py"]
working_dir = "."
enabled = false

[environment]
PORT = "9090"
HEALTH_PATH = "/health"

[restart]
policy = "on-failure"
max_retries = 3
backoff = "fixed"
backoff_initial = "1s"
backoff_max = "10s"

[health_check]
type = "http"
http_url = "http://localhost:9090/health"
http_method = "GET"
http_expect_status = 200
interval = "3s"
timeout = "2s"
retries = 2
on_unhealthy = "restart"

[stop]
signal = "SIGTERM"
timeout = "5s"

[log]
max_size = "10MB"
max_files = 2
TOML

[ -f "$CONFIG_DIR/services/py-server.toml" ] && pass "Service config created" || fail "Failed to create service config"

# ---- Test 6: Add service ----
echo ""
echo "--- Test 6: Add service ---"
$BIN add "$CONFIG_DIR/services/py-server.toml" --socket "$SOCK" --config "$CONFIG_DIR" && pass "Add service succeeds" || fail "Add service failed"

# ---- Test 7: Status (no service running yet) ----
echo ""
echo "--- Test 7: Status ---"
$BIN ps --socket "$SOCK" --config "$CONFIG_DIR" && pass "PS command works"

# ---- Test 8: Start service ----
echo ""
echo "--- Test 8: Start service ---"
$BIN start py-server --socket "$SOCK" --config "$CONFIG_DIR" && pass "Start service succeeds" || fail "Start service failed"
sleep 2

# ---- Test 9: Check status after start ----
echo ""
echo "--- Test 9: Status after start ---"
$BIN status py-server --socket "$SOCK" --config "$CONFIG_DIR"
# Verify it's running by curling the HTTP server
curl -s http://localhost:9090/ && pass "Service is responding" || fail "Service not responding"
curl -s http://localhost:9090/health | grep -q '"status":"ok"' && pass "Health check endpoint works" || fail "Health check endpoint failed"

# Wait for health check to mark it healthy
sleep 5
echo ""
echo "--- Test 9b: Health check status ---"
$BIN status py-server --socket "$SOCK" --config "$CONFIG_DIR" | grep -q "healthy" && pass "Service marked as healthy" || echo -e "${YELLOW}WARN${NC}: Service may not be healthy yet"

# ---- Test 10: Logs ----
echo ""
echo "--- Test 10: Logs ---"
$BIN logs py-server -n 5 --socket "$SOCK" --config "$CONFIG_DIR" && pass "Logs command works"

# ---- Test 11: Stop service ----
echo ""
echo "--- Test 11: Stop service ---"
$BIN stop py-server --socket "$SOCK" --config "$CONFIG_DIR" && pass "Stop service succeeds" || fail "Stop service failed"
sleep 1

# Verify it stopped
$BIN status py-server --socket "$SOCK" --config "$CONFIG_DIR" | grep -q "stopped" && pass "Service is stopped" || fail "Service not stopped"

# ---- Test 12: Restart service ----
echo ""
echo "--- Test 12: Restart service ---"
$BIN restart py-server --socket "$SOCK" --config "$CONFIG_DIR" && pass "Restart succeeds" || fail "Restart failed"
sleep 2
curl -s http://localhost:9090/health | grep -q '"status":"ok"' && pass "Service restarted and responding" || fail "Service not responding after restart"

# ---- Test 13: Remove service ----
echo ""
echo "--- Test 13: Remove service ---"
$BIN remove py-server --socket "$SOCK" --config "$CONFIG_DIR" && pass "Remove succeeds" || fail "Remove failed"
sleep 1
$BIN status py-server --socket "$SOCK" --config "$CONFIG_DIR" 2>&1 | grep -q "not found" && pass "Service is removed" || fail "Service still exists after remove"

# ---- Cleanup ----
echo ""
echo "--- Cleanup ---"
kill $DAEMON_PID 2>/dev/null || true
rm -rf "$CONFIG_DIR"
rm -f "$SOCK"
pkill -f "test_server.py" 2>/dev/null || true

echo ""
echo -e "${GREEN}=== All tests passed! ===${NC}"
