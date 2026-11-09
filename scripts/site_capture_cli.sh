#!/usr/bin/env bash
#
# Record what the prot CLI really prints, for the terminals on the landing page.
# Runs a throwaway protd on the default ports (8080 and 4400) with its own data directory and a fake ssh, then
# writes one .txt per command into the output directory. Stop your own protd first: this refuses to run while
# the ports are taken. Afterwards: scripts/gen_site_assets.py <ui-captures> <this output dir>
#
# Usage: scripts/site_capture_cli.sh [output-dir]
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
OUT="${1:-/tmp/cap/out}"
if ss -ltn | grep -qE ':(8080|4400|5173|8000|8001) '; then
    echo "a port in 8080/4400/5173/8000/8001 is busy; stop protd and the dev servers first" >&2
    exit 1
fi
mkdir -p "$OUT" "$WORK/bin" "$WORK/data" "$WORK/shop" "$WORK/www"
cleanup() { kill "${PROTD:-0}" 2>/dev/null || true; sleep 1; rm -rf "$WORK"; }
trap cleanup EXIT

(cd "$ROOT" && go build -o "$WORK/protd" ./cmd/protd && go build -o "$WORK/prot" ./cmd/prot)
cat > "$WORK/bin/ssh" <<'EOF'
#!/bin/sh
for a in "$@"; do case "$a" in 127.0.0.1:*:*:*) spec=$a;; esac; done
port=$(echo "$spec" | cut -d: -f2)
exec python3 -c "
import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',$port)); s.listen(5); time.sleep(300)"
EOF
chmod +x "$WORK/bin/ssh"
echo '<title>shop</title>' > "$WORK/shop/index.html"; cp "$WORK/shop/index.html" "$WORK/www/"
echo '{"name":"shop","scripts":{"dev":"python3 -m http.server 5173"}}' > "$WORK/shop/package.json"

export PROT_DATA_DIR="$WORK/data"
PATH="$WORK/bin:$PATH" "$WORK/protd" -data-dir "$WORK/data" > "$WORK/protd.log" 2>&1 &
PROTD=$!
sleep 2
P="$WORK/prot"
$P add "Storefront" --cmd "npm run dev" --alias shop --port 5173 --cwd "$WORK/shop" > "$OUT/add1.txt" 2>&1
$P add "Billing API" --cmd "python3 -m http.server 8000" --alias api --port 8000 --cwd "$WORK/www" > "$OUT/add2.txt" 2>&1
$P add "Docs" --cmd "false" --alias docs --port 8001 > "$OUT/add3.txt" 2>&1
$P start shop > "$OUT/start1.txt" 2>&1
$P start api > "$OUT/start2.txt" 2>&1
$P start docs > "$OUT/start3.txt" 2>&1
sleep 3
$P ls > "$OUT/ls.txt" 2>&1
$P status > "$OUT/status.txt" 2>&1
pid=$($P ls --json | python3 -c "import json,sys; print([s for s in json.load(sys.stdin) if s['alias']=='api'][0]['pid'])")
kill -9 "$pid"
sleep 3.5
$P logs api > "$OUT/logs_api.txt" 2>&1
$P logs docs > "$OUT/logs_docs.txt" 2>&1
$P ls > "$OUT/ls2.txt" 2>&1
PATH="$WORK/bin:$PATH" timeout 30 $P tunnel add proxmox --host root@192.168.0.109 --remote-port 8006 \
    --auto-connect > "$OUT/tunnel_add.txt" 2>&1
$P tunnel ls > "$OUT/tunnel_ls.txt" 2>&1
for s in shop api docs; do $P stop "$s" > /dev/null 2>&1 || true; done
echo "wrote $OUT"
