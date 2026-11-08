#!/usr/bin/env python3
"""Render docs/assets/architecture.svg in the prot desktop design system.

Fonts are embedded as data URIs so the SVG renders the same everywhere (GitHub
shows SVGs through <img>, which blocks external font files).
"""
import base64
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent
FONT_DIR = ROOT / "apps/desktop/src/assets/fonts"
OUT = ROOT / "docs/assets/architecture.svg"

W, H = 960, 580
LIGHT, DARK, INK = "#9aa092", "#0a0b0c", "#000"


def b64(name: str) -> str:
    return base64.b64encode((FONT_DIR / name).read_bytes()).decode()


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;")


def bevel(x, y, w, h, t=3, raised=True):
    a, b = (LIGHT, DARK) if raised else (DARK, LIGHT)
    h2 = t / 2
    return (
        f'<polyline points="{x+h2},{y+h-h2} {x+h2},{y+h2} {x+w-h2},{y+h2}" fill="none" stroke="{a}" '
        f'stroke-width="{t}"/>'
        f'<polyline points="{x+h2},{y+h-h2} {x+w-h2},{y+h-h2} {x+w-h2},{y+h2}" fill="none" stroke="{b}" '
        f'stroke-width="{t}"/>'
    )


def window(x, y, w, h, title, led="#5af451", crashed=False):
    bar = "url(#bar-crash)" if crashed else "url(#bar)"
    o = []
    o.append(f'<rect x="{x-5}" y="{y+5}" width="{w}" height="{h}" fill="#000" opacity="0.6"/>')
    o.append(f'<rect x="{x-2}" y="{y-2}" width="{w+4}" height="{h+4}" fill="{INK}"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="url(#metal)"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="28" fill="{bar}"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="1" fill="#fff" opacity="0.35"/>')
    o.append(bevel(x, y, w, h))
    o.append(
        f'<text x="{x+13}" y="{y+21}" class="wa" fill="#000">{esc(title)}</text>'
        f'<text x="{x+12}" y="{y+20}" class="wa" fill="#fff">{esc(title)}</text>'
    )
    # LED in a sunken pill
    px = x + w - 38
    o.append(f'<rect x="{px}" y="{y+6}" width="30" height="16" fill="#000"/>')
    o.append(bevel(px, y + 6, 30, 16, 2, raised=False))
    o.append(f'<circle cx="{px+15}" cy="{y+14}" r="4.5" fill="{led}" filter="url(#glow)"/>')
    o.append(f'<circle cx="{px+13.5}" cy="{y+12.5}" r="1.4" fill="#fff" opacity="0.9"/>')
    return "".join(o)


def well(x, y, w, h, head, lines):
    o = [f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#000"/>', bevel(x, y, w, h, 3, raised=False)]
    o.append(f'<text x="{x+12}" y="{y+25}" class="wa gold">{esc(head)}</text>')
    for i, ln in enumerate(lines):
        o.append(f'<text x="{x+12}" y="{y+46+i*17}" class="mono {"grn" if i == 0 else "dim"}">{esc(ln)}</text>')
    return "".join(o)


def stripe_rect(x, y, w, h):
    return (
        f'<rect x="{x-2}" y="{y-2}" width="{w+4}" height="{h+4}" fill="{INK}"/>'
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="url(#hazard)"/>'
    )


def arrow_head(cx, y, size=22):
    pts = f"{cx-size},{y} {cx+size},{y} {cx},{y+size*1.1}"
    return f'<polygon points="{pts}" fill="{INK}" transform="translate(0,-2)"/>' + (
        f'<polygon points="{cx-size+4},{y} {cx+size-4},{y} {cx},{y+size*1.1-6}" fill="url(#hazard)"/>'
    )


parts = []

# --- clients row -----------------------------------------------------------
CW, CH, CY = 270, 108, 36
cx = [45, 345, 645]
clients = [
    ("PROT DESKTOP", ["Tauri 2 + React", "portrait GUI client"]),
    ("PROT CLI", ["terminal control", "diagnostics + MCP stdio"]),
    ("AI AGENTS", ["MCP clients", "Claude, Cursor, pi"]),
]
for x, (title, lines) in zip(cx, clients):
    parts.append(window(x, CY, CW, CH, title))
    parts.append(f'<rect x="{x+12}" y="{CY+40}" width="{CW-24}" height="{CH-52}" fill="#000"/>')
    parts.append(bevel(x + 12, CY + 40, CW - 24, CH - 52, 3, raised=False))
    parts.append(f'<text x="{x+24}" y="{CY+64}" class="mono grn">&gt; {esc(lines[0])}</text>')
    parts.append(f'<text x="{x+24}" y="{CY+84}" class="mono dim">{esc(lines[1])}</text>')

# --- connectors ------------------------------------------------------------
BUS_Y = 196
stems = [x + CW // 2 for x in cx]
for sx in stems:
    parts.append(stripe_rect(sx - 5, CY + CH + 4, 10, BUS_Y - (CY + CH) - 4 + 5))
parts.append(stripe_rect(stems[0] - 5, BUS_Y - 5, stems[2] - stems[0] + 10, 10))

# label chip on the bus
chip_w, chip_h = 250, 34
chip_x, chip_y = W // 2 - chip_w // 2, BUS_Y - chip_h // 2
parts.append(f'<rect x="{chip_x-5}" y="{chip_y+5}" width="{chip_w}" height="{chip_h}" fill="#000" opacity="0.6"/>')
parts.append(f'<rect x="{chip_x}" y="{chip_y}" width="{chip_w}" height="{chip_h}" fill="#000"/>')
parts.append(bevel(chip_x, chip_y, chip_w, chip_h, 3, raised=False))
parts.append(f'<circle cx="{chip_x+20}" cy="{BUS_Y}" r="5" fill="#5af451" filter="url(#glow)"/>')
parts.append(f'<circle cx="{chip_x+18.5}" cy="{BUS_Y-1.5}" r="1.5" fill="#fff"/>')
parts.append(
    f'<text x="{chip_x+36}" y="{BUS_Y+5}" class="mono led" filter="url(#glow)">REST API · IPC · MCP</text>'
    f'<text x="{chip_x+36}" y="{BUS_Y+5}" class="mono led">REST API · IPC · MCP</text>'
)

# main stem to protd
PY = 272
parts.append(stripe_rect(W // 2 - 5, BUS_Y + chip_h // 2 + 2, 10, PY - (BUS_Y + chip_h // 2) - 22))
parts.append(arrow_head(W // 2, PY - 24, 20))

# --- protd -------------------------------------------------------------------
PX, PW = 45, 870
SW, SH, GAP = 270, 82, 15
PH = 28 + 20 + 2 * SH + 18 + 20
parts.append(window(PX, PY, PW, PH, "PROTD · LONG-RUNNING BACKGROUND DAEMON"))
subs = [
    ("SERVICE REGISTRY", ["SQLite, WAL mode", "services, routes, tunnels"]),
    ("REVERSE PROXY", ["*.localhost gateway", "WebSockets + SSE streaming"]),
    ("PROCESS DISCOVERY", ["listening TCP sockets", "bounded HTTP probes"]),
    ("PROCESS SUPERVISOR", ["child process groups", "bounded in-memory logs"]),
    ("SSH TUNNELS", ["OpenSSH supervision", "auto reconnect + backoff"]),
    ("SECURITY", ["loopback only, 256-bit token", "DNS-rebinding protection"]),
]
for i, (head, lines) in enumerate(subs):
    col, row = i % 3, i // 3
    parts.append(well(PX + 15 + col * (SW + GAP), PY + 28 + 20 + row * (SH + 18), SW, SH, head, lines))

# footer stripe
FY = PY + PH + 22
parts.append(stripe_rect(45, FY, 870, 10))

body = "\n".join(parts)

svg = f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-labelledby="t d">
<title id="t">prot architecture</title>
<desc id="d">Prot Desktop, the prot CLI and AI agents talk over a REST API, IPC and MCP to protd, a long-running daemon that owns the service registry, reverse proxy, process discovery, process supervisor, SSH tunnels and security layer.</desc>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{b64("anton.woff2")}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:400 700;src:url(data:font/woff2;base64,{b64("jetbrains-mono.woff2")}) format('woff2');}}
.wa{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:17px;letter-spacing:1.4px}}
.gold{{fill:#ffd21a;font-size:16px}}
.mono{{font-family:'JetBrains Mono',ui-monospace,monospace;font-size:12.5px}}
.grn{{fill:#5af451}}
.dim{{fill:#9aa092}}
.led{{fill:#5af451;font-weight:700;font-size:14px;letter-spacing:1px}}
</style>
<linearGradient id="bar" x1="0" x2="1" y1="0" y2="0"><stop offset="0" stop-color="#4a1400"/><stop offset=".55" stop-color="#b53a05"/><stop offset="1" stop-color="#fb8808"/></linearGradient>
<linearGradient id="bar-crash" x1="0" x2="1" y1="0" y2="0"><stop offset="0" stop-color="#2a0000"/><stop offset=".55" stop-color="#8a0d10"/><stop offset="1" stop-color="#e0301a"/></linearGradient>
<linearGradient id="metal" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="#34383a"/><stop offset="1" stop-color="#24282a"/></linearGradient>
<radialGradient id="glowbg" cx=".5" cy="0" r=".7"><stop offset="0" stop-color="#b03a05" stop-opacity=".35"/><stop offset="1" stop-color="#b03a05" stop-opacity="0"/></radialGradient>
<pattern id="dots" width="6" height="6" patternUnits="userSpaceOnUse"><circle cx="3" cy="3" r=".6" fill="#fff" opacity=".07"/></pattern>
<pattern id="hazard" width="16" height="16" patternUnits="userSpaceOnUse" patternTransform="rotate(-45)"><rect width="16" height="16" fill="#050608"/><rect width="8" height="16" fill="#fb8808"/></pattern>
<filter id="glow" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="2.2"/></filter>
</defs>
<rect width="{W}" height="{H}" fill="#0d0a09"/>
<rect width="{W}" height="{H}" fill="url(#glowbg)"/>
<rect width="{W}" height="{H}" fill="url(#dots)"/>
<rect x="2" y="2" width="{W-4}" height="{H-4}" fill="none" stroke="#000" stroke-width="4"/>
{body}
</svg>
"""
OUT.write_text(svg)
print(f"wrote {OUT} ({len(svg)//1024} KiB)")
