#!/bin/bash
set -e

# Directory of this script
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
ROOT_DIR="$DIR/../.."
BIN_DIR="$ROOT_DIR/bin"

# Cleanup function
cleanup() {
  echo "Cleaning up..."
  if [ -n "$SERVER_PID" ]; then
    kill $SERVER_PID || true
  fi
  if [ -n "$APP_PID" ]; then
    kill $APP_PID || true
  fi
}
trap cleanup EXIT

# Build Portal Server
echo "Building Portal Server..."
cd "$ROOT_DIR"
make build-wasm
make build-frontend
make build-server

# Start Portal Server
echo "Starting Portal Server..."
export PORTAL_UI_URL="http://localhost:4017"
export PORTAL_FRONTEND_URL="http://*.localhost:4017"
export BOOTSTRAP_URIS="ws://localhost:4017/relay"
"$BIN_DIR/relay-server" --port 4017 &
SERVER_PID=$!
sleep 2 # Wait for server to start

# Build Test App (Go)
echo "Building Test App..."
cd "$DIR/test-app"
go mod init test-app || true
go mod tidy
go build -o test-app main.go

# Start Test App
echo "Starting Test App..."
./test-app --relay "ws://localhost:4017/relay" --port 3000 > app.log 2>&1 &
APP_PID=$!
sleep 2 # Wait for app to register



APP_URL="http://test-app.localhost:4017"
echo "App URL: $APP_URL"

# Run Puppeteer Tests
echo "Running Puppeteer Tests..."
export TARGET_URL="$APP_URL"
cd "$DIR/puppeteer"
npm install
npm test
