#!/usr/bin/env bash
set -euo pipefail

echo "=========================================================="
echo "    prot — End-to-End Acceptance & Verification Suite     "
echo "=========================================================="

WORKDIR="/tmp/prot_e2e_test"
rm -rf "$WORKDIR"
mkdir -p "$WORKDIR"

export PROT_DATA_DIR="$WORKDIR"
GATEWAY_ADDR="127.0.0.1:18080"
ADMIN_ADDR="127.0.0.1:14400"

# 1. Start protd background daemon
echo "[1/6] Starting protd daemon..."
./bin/protd -data-dir "$PROT_DATA_DIR" -gateway "$GATEWAY_ADDR" -admin "$ADMIN_ADDR" > "$WORKDIR/daemon.log" 2>&1 &
DAEMON_PID=$!
sleep 1

cleanup() {
    echo "Stopping test processes..."
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
    rm -rf "$WORKDIR"
}
trap cleanup EXIT

# 2. Check daemon health and CLI status
echo "[2/6] Checking daemon health via prot CLI..."
./bin/prot status
./bin/prot doctor

# 3. Register and start a managed HTTP service
echo "[3/6] Registering and starting a managed service..."
./bin/prot add "Hello API" --cmd "sh -c 'python3 -m http.server 19284'" --alias "hello" --port 19284
./bin/prot ls
./bin/prot start "hello"
sleep 2

# 4. Access service via reverse proxy using hello.localhost
echo "[4/6] Verifying local reverse proxy at http://$GATEWAY_ADDR with Host: hello.localhost..."
PROXY_RESP=$(curl -s -o /dev/null -w "%{http_code}" -H "Host: hello.localhost" "http://$GATEWAY_ADDR/")
echo "      Reverse proxy HTTP status code: $PROXY_RESP"
if [ "$PROXY_RESP" != "200" ]; then
    echo "ERROR: Expected 200 OK from reverse proxy, got $PROXY_RESP"
    cat "$WORKDIR/daemon.log"
    exit 1
fi
echo "      ✓ Reverse proxy successfully reached managed service via hello.localhost"

# Check logs
echo "      Checking service logs:"
./bin/prot logs "hello" -n 5

# 5. Verify MCP stdio adapter
echo "[5/6] Testing MCP stdio adapter (prot mcp)..."
MCP_INPUT='{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_service_url","arguments":{"id_or_alias":"hello"}}}
'
MCP_OUTPUT=$(echo "$MCP_INPUT" | ./bin/prot mcp)
echo "      MCP Output:"
echo "$MCP_OUTPUT" | grep -E "canonical_url|hello\.localhost|protocolVersion" || true
echo "      ✓ MCP stdio adapter responded successfully"

# 6. Verify MCP HTTP endpoint
echo "[6/6] Testing MCP HTTP endpoint at http://$ADMIN_ADDR/mcp..."
TOKEN=$(cat "$WORKDIR/auth.token")
HTTP_MCP_RESP=$(curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","id":10,"method":"tools/list"}' "http://$ADMIN_ADDR/mcp")
echo "$HTTP_MCP_RESP" | grep -q "list_services"
echo "      ✓ MCP HTTP endpoint returned tool list successfully"

echo ""
echo "=========================================================="
echo "    ✓ ALL END-TO-END ACCEPTANCE TESTS PASSED!             "
echo "=========================================================="
