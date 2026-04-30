.PHONY: build clean test test-daemon test-cli install schema lint

BINARY=bin/workerd

build: schema
	go build -o $(BINARY) ./cmd/workerd/

schema:
	go run ./cmd/workerd/ schema > schemas/workerd.schema.json

clean:
	rm -rf bin/
	rm -f /tmp/workerd-test.sock
	rm -rf /tmp/workerd-test

install:
	go install ./cmd/workerd/

lint:
	gofmt -w .
	go vet ./...

# Start daemon in foreground for testing
test-daemon: build
	./bin/workerd daemon --foreground \
		--socket /tmp/workerd-test.sock \
		--config /tmp/workerd-test

# Run CLI test
test-cli: build
	./test/run_tests.sh

# All tests
test: build test-cli

# Quick smoke test with Python HTTP server
smoke-test: build
	@echo "=== Smoke Test ==="
	@echo "Starting daemon..."
	./bin/workerd daemon --socket /tmp/workerd-test.sock --config /tmp/workerd-test &
	@sleep 1
	@echo "Status:"
	./bin/workerd ps --socket /tmp/workerd-test.sock --config /tmp/workerd-test
	@echo "Stopping daemon..."
	./bin/workerd shutdown --socket /tmp/workerd-test.sock --config /tmp/workerd-test || kill $$(cat /tmp/workerd-test/workerd.pid) 2>/dev/null || true
