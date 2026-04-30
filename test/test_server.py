#!/usr/bin/env python3
"""
Simple HTTP server for testing workerd process management.
Listens on the specified port and responds with a status page.
"""
import http.server
import os
import sys
import time
import signal

PORT = int(os.environ.get("PORT", "8080"))
HEALTH_PATH = os.environ.get("HEALTH_PATH", "/health")


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == HEALTH_PATH:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status":"ok"}\n')
        else:
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            msg = f"Hello from PID {os.getpid()}, path: {self.path}\n"
            self.wfile.write(msg.encode())

    def log_message(self, format, *args):
        print(f"[{time.strftime('%H:%M:%S')}] {args[0]}", flush=True)


def main():
    print(f"Starting HTTP server on port {PORT}", flush=True)

    def handle_signal(signum, frame):
        print(f"Received signal {signum}, shutting down...", flush=True)
        sys.exit(0)

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)

    server = http.server.HTTPServer(("127.0.0.1", PORT), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
