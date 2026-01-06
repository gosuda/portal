#!/bin/bash

# V2 Multipath Routing Test Script
# This script automates the full multipath routing test

set -e

echo "========================================="
echo "  V2 Multipath Routing Test"
echo "========================================="
echo ""

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test configuration
SERVER1_PORT=4217
SERVER2_PORT=4218
SERVER3_PORT=4219
TEST_DURATION=30

# Cleanup function
cleanup() {
    echo ""
    echo -e "${YELLOW}[TEST] Cleaning up...${NC}"
    kill $(jobs -p) 2>/dev/null || true
    wait 2>/dev/null || true
    echo -e "${GREEN}[TEST] Cleanup complete${NC}"
}

# Set cleanup trap
trap cleanup EXIT INT TERM

# Test 1: Single Server with Multiple Paths
echo -e "${YELLOW}[TEST 1] Single Server with Multiple Paths${NC}"
echo "----------------------------------------------"

# Start one server
echo "Starting test server on port $SERVER1_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER1_PORT > /tmp/server1.log 2>&1 &
SERVER1_PID=$!
sleep 2

# Start client with 2 paths to the same server (simulated with different ports)
echo "Starting test client with 2 paths..."
go run ./cmd/test-client-v2/ -relay 127.0.0.1:$SERVER1_PORT,127.0.0.1:$SERVER2_PORT > /tmp/client1.log 2>&1 &
CLIENT1_PID=$!
sleep 3

echo "Waiting 10 seconds for statistics..."
sleep 10

echo ""
echo -e "${GREEN}=== Client Statistics ===${NC}"
tail -20 /tmp/client1.log 2>/dev/null | grep "Statistics" -A 8 || true

echo ""
echo -e "${GREEN}=== Server Statistics ===${NC}"
tail -20 /tmp/server1.log 2>/dev/null | grep "Statistics" -A 6 || true

# Cleanup
kill $SERVER1_PID $CLIENT1_PID 2>/dev/null || true
wait $SERVER1_PID $CLIENT1_PID 2>/dev/null || true

echo -e "${GREEN}[TEST 1] Complete${NC}"
echo ""
sleep 2

# Test 2: Multiple Servers with Path Switching
echo -e "${YELLOW}[TEST 2] Multiple Servers with Path Switching${NC}"
echo "-------------------------------------------------"

# Start multiple servers
echo "Starting test server 1 on port $SERVER1_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER1_PORT > /tmp/server1.log 2>&1 &
SERVER1_PID=$!
sleep 1

echo "Starting test server 2 on port $SERVER2_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER2_PORT > /tmp/server2.log 2>&1 &
SERVER2_PID=$!
sleep 1

# Start client with auto-switch enabled
echo "Starting test client with auto-switch to 3 servers..."
go run ./cmd/test-client-v2/ -relay 127.0.0.1:$SERVER1_PORT,127.0.0.1:$SERVER2_PORT,127.0.0.1:$SERVER3_PORT -auto-switch=true > /tmp/client2.log 2>&1 &
CLIENT2_PID=$!
sleep 3

echo "Running for 15 seconds to observe path switching..."
for i in {1..15}; do
    echo -n "."
    sleep 1
done
echo ""

echo ""
echo -e "${GREEN}=== Client Statistics (Auto-switch) ===${NC}"
tail -50 /tmp/client2.log 2>/dev/null | grep "SWITCHING" || true
echo ""
tail -20 /tmp/client2.log 2>/dev/null | grep "Statistics" -A 8 || true

echo ""
echo -e "${GREEN}=== Server Statistics ===${NC}"
echo "Server 1:"
tail -20 /tmp/server1.log 2>/dev/null | grep "Statistics" -A 6 || true
echo ""
echo "Server 2:"
tail -20 /tmp/server2.log 2>/dev/null | grep "Statistics" -A 6 || true

# Cleanup
kill $SERVER1_PID $SERVER2_PID $CLIENT2_PID 2>/dev/null || true
wait 2>/dev/null || true

echo -e "${GREEN}[TEST 2] Complete${NC}"
echo ""
sleep 2

# Test 3: Manual Path Switching
echo -e "${YELLOW}[TEST 3] Manual Path Switching${NC}"
echo "------------------------------------"

# Start servers
echo "Starting test server 1 on port $SERVER1_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER1_PORT > /tmp/server1.log 2>&1 &
SERVER1_PID=$!
sleep 1

echo "Starting test server 2 on port $SERVER2_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER2_PORT > /tmp/server2.log 2>&1 &
SERVER2_PID=$!
sleep 1

echo "Starting test server 3 on port $SERVER3_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER3_PORT > /tmp/server3.log 2>&1 &
SERVER3_PID=$!
sleep 1

# Start client without auto-switch
echo "Starting test client with manual switching..."
go run ./cmd/test-client-v2/ -relay 127.0.0.1:$SERVER1_PORT,127.0.0.1:$SERVER2_PORT,127.0.0.1:$SERVER3_PORT -auto-switch=false > /tmp/client3.log 2>&1 &
CLIENT3_PID=$!
sleep 3

echo ""
echo -e "${YELLOW}Demonstrating manual path switching...${NC}"
echo "Waiting for initial connection..."
sleep 2

# Simulate manual switches
echo ""
echo -e "${GREEN}[Manual Switch Test]${NC}"
echo "Sending '1' to switch to Path 1..."
echo "1" > /tmp/client_input.log

echo ""
echo -e "${YELLOW}Waiting 5 seconds...${NC}"
sleep 5

echo ""
echo "Sending '2' to switch to Path 2..."
echo "2" > /tmp/client_input.log

echo ""
echo -e "${YELLOW}Waiting 5 seconds...${NC}"
sleep 5

echo ""
echo "Sending '3' to switch to Path 3..."
echo "3" > /tmp/client_input.log

echo ""
echo -e "${YELLOW}Waiting 5 seconds...${NC}"
sleep 5

echo ""
tail -30 /tmp/client3.log 2>/dev/null | grep "Statistics" -A 8 || true

# Cleanup
kill $SERVER1_PID $SERVER2_PID $SERVER3_PID $CLIENT3_PID 2>/dev/null || true
wait 2>/dev/null || true

echo -e "${GREEN}[TEST 3] Complete${NC}"
echo ""
sleep 2

# Test 4: Connection Failure and Recovery
echo -e "${YELLOW}[TEST 4] Connection Failure and Recovery${NC}"
echo "-------------------------------------------"

# Start server
echo "Starting test server on port $SERVER1_PORT..."
go run ./cmd/test-server-v2/ -port $SERVER1_PORT -debug > /tmp/server1.log 2>&1 &
SERVER1_PID=$!
sleep 2

# Start client with 2 paths (one invalid)
echo "Starting test client with one valid and one invalid path..."
go run ./cmd/test-client-v2/ -relay 127.0.0.1:$SERVER1_PORT,127.0.0.1:9999,127.0.0.1:$SERVER2_PORT -auto-switch=true > /tmp/client4.log 2>&1 &
CLIENT4_PID=$!
sleep 5

echo ""
echo -e "${GREEN}=== Client Statistics (with invalid path) ===${NC}"
tail -30 /tmp/client4.log 2>/dev/null | grep "Statistics" -A 8 || true

# Cleanup
kill $SERVER1_PID $CLIENT4_PID 2>/dev/null || true
wait 2>/dev/null || true

echo -e "${GREEN}[TEST 4] Complete${NC}"
echo ""
sleep 2

# Summary
echo ""
echo "========================================="
echo -e "${GREEN}  All Tests Complete!${NC}"
echo "========================================="
echo ""
echo "Test logs saved to:"
echo "  - /tmp/server1.log"
echo "  - /tmp/server2.log"
echo "  - /tmp/server3.log"
echo "  - /tmp/client1.log"
echo "  - /tmp/client2.log"
echo "  - /tmp/client3.log"
echo "  - /tmp/client4.log"
echo ""
echo "To view logs:"
echo "  tail -f /tmp/client2.log  # Real-time client stats"
echo "  grep 'SWITCHING' /tmp/client2.log  # View path switches"
echo "  grep 'Statistics' /tmp/*.log  # View statistics"
echo ""
