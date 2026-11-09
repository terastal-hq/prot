#!/usr/bin/env python3
"""Render docs/assets/before-after.svg: an animated before/after story in the prot design system.

Before: terminals fighting over ports (real Next.js / Node / lsof output) and a row of indistinguishable
        localhost:<port> browser tabs.
After:  one prot window, real prot CLI output, and tabs named after the services.

No slogans: every line of text is something the tools actually print.

Everything loops on one 18 second CSS timeline (no scripts, so it also runs through GitHub's <img>). Fonts are
subset and embedded as data URIs. Without animation (prefers-reduced-motion, or a renderer that ignores CSS
animation) only the finished "after" scene is shown.

Requires: pip install fonttools brotli
"""
import base64
import io
import pathlib

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = pathlib.Path(__file__).resolve().parent.parent
FONT_DIR = ROOT / "apps/desktop/src/assets/fonts"
OUT = ROOT / "docs/assets/before-after.svg"

W, H = 520, 588
T = 18.0  # loop length in seconds
LIGHT, DARK, INK = "#9aa092", "#0a0b0c", "#000"
GLYPHS = "".join(chr(c) for c in range(32, 127)) + "·—"


# --------------------------------------------------------------------------- helpers
def font_b64(name: str) -> str:
    opts = subset.Options()
    opts.flavor = "woff2"
    opts.layout_features = ["kern"]
    font = TTFont(str(FONT_DIR / name))
    sub = subset.Subsetter(opts)
    sub.populate(text=GLYPHS)
    sub.subset(font)
    font.flavor = "woff2"
    buf = io.BytesIO()
    font.save(buf)
    return base64.b64encode(buf.getvalue()).decode()


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;")


def pct(t: float) -> str:
    return f"{max(0.0, min(100.0, t / T * 100)):.3f}%"


KEYFRAMES: list[str] = []
CSS_RULES: list[str] = []
_n = [0]


def animated(stops, ease="ease-out", default="") -> str:
    """Register keyframes from (seconds, css) stops and return a class that plays them once per loop.

    `default` is the resting style when animation is off, so it should describe the finished state.
    """
    _n[0] += 1
    name = f"k{_n[0]}"
    KEYFRAMES.append(f"@keyframes {name}{{" + "".join(f"{pct(t)}{{{css}}}" for t, css in stops) + "}")
    cls = f"a{_n[0]}"
    CSS_RULES.append(f".{cls}{{animation:{name} {T}s {ease} infinite;{default}}}")
    return cls


def enter(t0: float, dur: float = 0.5, frm: str = "translate(0px,0px)") -> str:
    """Hidden until t0, then slides/fades to its resting place and stays."""
    return animated(
        [
            (0, f"opacity:0;transform:{frm}"),
            (t0, f"opacity:0;transform:{frm}"),
            (t0 + dur, "opacity:1;transform:translate(0px,0px)"),
            (T, "opacity:1;transform:translate(0px,0px)"),
        ]
    )


def swap(t_on: float, t_off: float | None, fade: float = 0.25) -> str:
    """Fade in at t_on and, if given, out again at t_off. An element that switches off stays hidden without animation."""
    stops = [(0, "opacity:0"), (max(t_on - fade, 0), "opacity:0"), (t_on, "opacity:1")]
    if t_off is None:
        stops.append((T, "opacity:1"))
        return animated(stops, "linear")
    stops += [(t_off, "opacity:1"), (t_off + fade, "opacity:0"), (T, "opacity:0")]
    return animated(stops, "linear", "opacity:0;")


def bevel(x, y, w, h, t=3, raised=True):
    a, b = (LIGHT, DARK) if raised else (DARK, LIGHT)
    h2 = t / 2
    return (
        f'<polyline points="{x+h2},{y+h-h2} {x+h2},{y+h2} {x+w-h2},{y+h2}" fill="none" stroke="{a}" '
        f'stroke-width="{t}"/>'
        f'<polyline points="{x+h2},{y+h-h2} {x+w-h2},{y+h-h2} {x+w-h2},{y+h2}" fill="none" stroke="{b}" '
        f'stroke-width="{t}"/>'
    )


def led(cx, cy, color="#5af451", r=4.5):
    return (
        f'<g class="blink"><circle cx="{cx}" cy="{cy}" r="{r}" fill="{color}" filter="url(#glow)"/>'
        f'<circle cx="{cx}" cy="{cy}" r="{r-0.5}" fill="{color}"/>'
        f'<circle cx="{cx-1.5}" cy="{cy-1.5}" r="1.4" fill="#fff" opacity="0.9"/></g>'
    )


def check(x, y, color="#5af451"):
    return f'<polyline points="{x},{y+5} {x+4},{y+9} {x+11},{y}" fill="none" stroke="{color}" stroke-width="2.4"/>'


def panel(x, y, w, h, fill="url(#metal)"):
    """Drop shadow, black outline and a raised bevel."""
    return (
        f'<rect x="{x-5}" y="{y+5}" width="{w}" height="{h}" fill="#000" opacity="0.55"/>'
        f'<rect x="{x-2}" y="{y-2}" width="{w+4}" height="{h+4}" fill="{INK}"/>'
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{fill}"/>' + bevel(x, y, w, h)
    )


def window(x, y, w, h, title, bar="url(#bar)", title_cls="wa"):
    return (
        panel(x, y, w, h)
        + f'<rect x="{x}" y="{y}" width="{w}" height="26" fill="{bar}"/>'
        + f'<rect x="{x}" y="{y}" width="{w}" height="1" fill="#fff" opacity="0.35"/>'
        + bevel(x, y, w, h)
        + f'<text x="{x+11}" y="{y+20}" class="{title_cls}" fill="#000">{esc(title)}</text>'
        + f'<text x="{x+10}" y="{y+19}" class="{title_cls}" fill="#fff">{esc(title)}</text>'
    )


def well(x, y, w, h):
    return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#000"/>' + bevel(x, y, w, h, 3, raised=False)


def typed(x, y, w, h, t0, dur, chars):
    """A black cover over text that shrinks to the right, revealing the text left to right."""
    cls = animated(
        [(0, "transform:scaleX(1)"), (t0, "transform:scaleX(1)"), (t0 + dur, "transform:scaleX(0)"), (T, "transform:scaleX(0)")],
        f"steps({chars},end)",
        "transform:scaleX(0);transform-box:fill-box;transform-origin:right center;",
    )
    return f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" fill="#000"/>'


# --------------------------------------------------------------------------- shared: browser tab strip
TAB_Y, TAB_H = 508, 40
STRIP_X, STRIP_W = 30, 460


def tab_strip(labels, t_in, favicon):
    """A row of browser tabs. Returns (svg, x of first tab, tab width, gap)."""
    gap = 3
    tw = (STRIP_W - 16 - gap * (len(labels) - 1)) / len(labels)
    o = [f'<g class="{enter(t_in, 0.4, "translate(0px,14px)")}">', panel(STRIP_X, TAB_Y, STRIP_W, TAB_H)]
    for i, label in enumerate(labels):
        tx = STRIP_X + 8 + i * (tw + gap)
        o.append(f'<rect x="{tx}" y="{TAB_Y+8}" width="{tw:.1f}" height="{TAB_H-14}" fill="#1b1e20"/>')
        o.append(bevel(tx, TAB_Y + 8, tw, TAB_H - 14, 2))
        o.append(f'<rect x="{tx+8}" y="{TAB_Y+17}" width="8" height="8" fill="{favicon(i)}"/>')
        o.append(f'<text x="{tx+22}" y="{TAB_Y+25}" class="mono tab">{esc(label)}</text>')
    o.append("</g>")
    return "".join(o), STRIP_X + 8, tw, gap


# --------------------------------------------------------------------------- scene: before
def terminal(x, y, rot, title, lines, t_in, crashed=False):
    w, h = 292, 108
    bar = "url(#bar-crash)" if crashed else "url(#bar-grey)"
    o = [f'<g transform="rotate({rot} {x + w/2} {y + h/2})"><g class="{enter(t_in, 0.45, "translate(-46px,0px)")}">']
    o.append(window(x, y, w, h, title, bar=bar, title_cls="ui"))
    o.append(well(x + 8, y + 34, w - 16, h - 42))
    for i, (txt, kind) in enumerate(lines):
        cls = f"mono term {kind}" + (" errblink" if kind == "err" else "")
        o.append(f'<text x="{x+18}" y="{y+55+i*16}" class="{cls}">{esc(txt)}</text>')
    o.append("</g></g>")
    return "".join(o)


def scene_before() -> str:
    o = [
        terminal(22, 94, -1.6, "~/web", [("$ npm run dev", "grn"), ("Port 3000 in use, trying 3001", "dim"), ("ready - localhost:3001", "dim")], 0.3),
        terminal(188, 188, 1.4, "~/api", [("$ lsof -i :8000", "grn"), ("python3  4127  you  TCP *:8000", "dim"), ("$ kill 4127", "grn")], 1.0),
        terminal(28, 282, 1.1, "~/docs", [("$ astro dev", "grn"), ("Local  http://localhost:4321/", "dim"), ("$ _", "grn")], 1.7),
        terminal(
            194,
            376,
            -1.5,
            "~/worker",
            [("$ node worker.js", "grn"), ("Error: listen EADDRINUSE:", "err"), ("address already in use :::3000", "err")],
            2.4,
            crashed=True,
        ),
    ]

    # Every tab says localhost:<something>; a highlight hops between them while you hunt for the right one.
    labels = ["localhost:3000", "localhost:3001", "localhost:8000", "localhost:4321"]
    strip, tx0, tw, gap = tab_strip(labels, 3.4, lambda i: "#8a9096")
    o.append(strip)

    hops = [0, 1, 0, 2, 1, 3]
    stops = [(0, "opacity:0;transform:translateX(0px)")]
    for k, idx in enumerate(hops):
        stops.append((5.0 + 0.55 * k, f"opacity:1;transform:translateX({idx * (tw + gap):.1f}px)"))
    stops += [(8.0, stops[-1][1]), (T, stops[-1][1])]
    hl = animated(stops, "steps(1,end)", "opacity:0;")
    o.append(
        f'<g class="{hl}"><rect x="{tx0}" y="{TAB_Y+8}" width="{tw:.1f}" height="{TAB_H-14}" fill="none" '
        f'stroke="#fb8808" stroke-width="2"/></g>'
    )
    return "".join(o)


# --------------------------------------------------------------------------- scene: after
def card(y, name, alias, port, t_in, port_before=None, port_change=0.0, crash_until=None):
    """A service card. port_before/port_change animate an auto-port restart; crash_until flips CRASHED to RUNNING."""
    x, w, h = 46, 428, 84
    o = [f'<g class="{enter(t_in, 0.45, "translate(36px,0px)")}">', panel(x, y, w, h)]
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="24" fill="url(#bar)"/>')
    if crash_until:
        c = animated(
            [(0, "opacity:1"), (crash_until, "opacity:1"), (crash_until + 0.35, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;"
        )
        o.append(f'<rect class="{c}" x="{x}" y="{y}" width="{w}" height="24" fill="url(#bar-crash)"/>')
    o.append(bevel(x, y, w, h))
    o.append(f'<text x="{x+11}" y="{y+18}" class="ui" fill="#000">{esc(name)}</text>')
    o.append(f'<text x="{x+10}" y="{y+17}" class="ui" fill="#fff">{esc(name)}</text>')

    bx = x + w - 104
    o.append(f'<rect x="{bx}" y="{y+4}" width="94" height="16" fill="#000"/>' + bevel(bx, y + 4, 94, 16, 2, raised=False))
    running = f'{led(bx + 12, y + 12)}<text x="{bx+24}" y="{y+16}" class="mono tiny grn">RUNNING</text>'
    if crash_until:
        up = animated(
            [(0, "opacity:0"), (crash_until, "opacity:0"), (crash_until + 0.35, "opacity:1"), (T, "opacity:1")], "linear"
        )
        down = animated(
            [(0, "opacity:1"), (crash_until, "opacity:1"), (crash_until + 0.35, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;"
        )
        o.append(f'<g class="{up}">{running}</g>')
        o.append(f'<g class="{down}">{led(bx + 12, y + 12, "#ff3b2f")}<text x="{bx+24}" y="{y+16}" class="mono tiny red">CRASHED</text></g>')
    else:
        o.append(f"<g>{running}</g>")

    o.append(well(x + 8, y + 32, w - 16, 44))
    o.append(f'<text x="{x+20}" y="{y+61}" class="url">http://{alias}.localhost<tspan class="urlport">:8080</tspan></text>')

    px = x + w - 20
    if port_before:
        gone = animated([(0, "opacity:1"), (port_change, "opacity:1"), (port_change + 0.06, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;")
        here = animated([(0, "opacity:0"), (port_change, "opacity:0"), (port_change + 0.06, "opacity:1"), (T, "opacity:1")], "linear")
        flash = animated(
            [(0, "opacity:0"), (port_change, "opacity:0"), (port_change + 0.1, "opacity:.9"), (port_change + 0.8, "opacity:0"), (T, "opacity:0")],
            "ease-out",
            "opacity:0;",
        )
        o.append(f'<rect class="{flash}" x="{px-78}" y="{y+38}" width="86" height="32" fill="#ffd21a"/>')
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold {gone}">:{port_before}</text>')
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold {here}">:{port}</text>')
    else:
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold">:{port}</text>')
    o.append("</g>")
    return "".join(o)


def cli_session(tx, ty, cmd, output, t_cmd, t_out, t_off=None):
    """A typed command and its output, as `prot` prints them. Visible from t_cmd until t_off (or the loop end)."""
    n = len(cmd)
    o = [
        f'<g class="{swap(t_cmd - 0.05, t_off, 0.15)}">',
        f'<text x="{tx+20}" y="{ty+29}" class="mono term grn">{esc(cmd)}</text>',
        typed(tx + 19, ty + 18, n * 6.9 + 4, 16, t_cmd, 0.07 * n + 0.3, n),
        f'<g class="{swap(t_out, None, 0.12)}">{check(tx+20, ty+39)}<text x="{tx+38}" y="{ty+48}" class="mono term dim">{esc(output)}</text></g>',
        "</g>",
    ]
    return "".join(o)


def scene_after() -> str:
    o = []
    wx, wy, ww, wh = 30, 92, 460, 328
    o.append(f'<g class="{enter(8.6, 0.5, "translate(0px,26px)")}">')
    o.append(window(wx, wy, ww, wh, "PROT"))
    cx = wx + ww - 150
    o.append(f'<rect x="{cx}" y="{wy+4}" width="140" height="18" fill="#000"/>' + bevel(cx, wy + 4, 140, 18, 2, raised=False))
    o.append(led(cx + 12, wy + 13))
    o.append(f'<text x="{cx+26}" y="{wy+17}" class="mono tiny grn">DAEMON ACTIVE</text>')
    o.append("</g>")

    o.append(card(134, "Web", "web", "3000", 9.1))
    o.append(card(228, "API", "api", "51724", 9.4, port_before="8000", port_change=16.6))
    o.append(card(322, "Docs", "docs", "4321", 9.7, crash_until=13.0))

    # Real `prot restart` output: first for the crashed service, then for the api (which picks a new port).
    tx, ty, tw, th = 30, 434, 460, 62
    o.append(f'<g class="{enter(10.4, 0.45, "translate(0px,18px)")}">{panel(tx, ty, tw, th)}{well(tx + 8, ty + 8, tw - 16, th - 16)}')
    o.append(cli_session(tx, ty, "$ prot restart docs", 'Restarted "Docs" (PID: 48213)', 11.0, 12.7, 14.5))
    o.append(cli_session(tx, ty, "$ prot restart api", 'Restarted "API" (PID: 48390)', 14.8, 16.4))
    o.append("</g>")

    # The tabs are named after the services, and they keep their names however the ports move.
    strip, *_ = tab_strip(["web.localhost:8080", "api.localhost:8080", "docs.localhost:8080"], 10.2, lambda i: "#5af451")
    o.append(strip)
    return "".join(o)


# --------------------------------------------------------------------------- assemble
# Without animation only the finished "after" scene shows (the before scene defaults to hidden).
before_vis = animated([(0, "opacity:1"), (8.0, "opacity:1"), (8.6, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;")
after_vis = animated(
    [(0, "opacity:0"), (8.3, "opacity:0"), (8.7, "opacity:1"), (17.5, "opacity:1"), (17.95, "opacity:0"), (T, "opacity:0")], "linear"
)
label_before = animated(
    [(0, "opacity:0"), (0.3, "opacity:1"), (8.0, "opacity:1"), (8.4, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;"
)
label_after = animated(
    [(0, "opacity:0"), (8.6, "opacity:0"), (9.0, "opacity:1"), (17.5, "opacity:1"), (17.95, "opacity:0"), (T, "opacity:0")], "linear"
)
# The band waits just off-screen left (x=-150) and sweeps across the stage between 7.9s and 9.0s.
wipe = animated(
    [(0, "transform:translateX(0px)"), (7.9, "transform:translateX(0px)"), (9.0, "transform:translateX(700px)"), (T, "transform:translateX(700px)")],
    "ease-in-out",
)


def plate_label(cls, text, color):
    return (
        f'<g class="{cls}"><text x="{W/2+2}" y="45" class="wa title" text-anchor="middle" fill="#000">{esc(text)}</text>'
        f'<text x="{W/2}" y="43" class="wa title" text-anchor="middle" fill="{color}">{esc(text)}</text></g>'
    )


before = scene_before()
after = scene_after()

svg = f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-labelledby="t d">
<title id="t">prot, before and after</title>
<desc id="d">Animation. Before: four terminals fighting over ports, with address-in-use errors, and browser tabs that all just say localhost and a port number. After: one prot window lists web, api and docs under stable localhost names, a crashed service is restarted with one command, and a restarted service picks a new port while its address stays the same.</desc>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{font_b64("anton.woff2")}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:400 700;src:url(data:font/woff2;base64,{font_b64("jetbrains-mono.woff2")}) format('woff2');}}
.wa{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:17px;letter-spacing:1.2px}}
.title{{font-size:25px;letter-spacing:1.6px}}
.gold{{fill:#ffd21a;font-size:24px}}
.ui{{font-family:Tahoma,Verdana,'DejaVu Sans',sans-serif;font-weight:700;font-size:13px;letter-spacing:.4px}}
.mono{{font-family:'JetBrains Mono',ui-monospace,monospace;font-size:12.5px}}
.term{{font-size:11.5px}}
.tab{{font-size:10px;fill:#c9cfc2}}
.tiny{{font-size:10.5px;font-weight:700;letter-spacing:.8px}}
.grn{{fill:#5af451}} .dim{{fill:#9aa092}} .red{{fill:#ff3b2f}} .err{{fill:#ff3b2f;font-weight:700}}
.url{{font-family:Tahoma,Verdana,'DejaVu Sans',sans-serif;font-weight:700;font-size:17px;fill:#8bd13a}}
.urlport{{font-size:13px;fill:#9aa092;font-weight:400}}
.blink{{animation:blink 1.6s ease-in-out infinite}}
.errblink{{animation:errblink .9s steps(2,jump-none) infinite}}
@keyframes blink{{0%,100%{{opacity:1}}50%{{opacity:.45}}}}
@keyframes errblink{{0%{{opacity:1}}100%{{opacity:.25}}}}
{chr(10).join(CSS_RULES)}
{chr(10).join(KEYFRAMES)}
@media (prefers-reduced-motion: reduce){{
  *{{animation:none !important}}
  .scene-before,.label-before,.wipe{{display:none}}
}}
</style>
<linearGradient id="bar" x1="0" x2="1" y1="0" y2="0"><stop offset="0" stop-color="#4a1400"/><stop offset=".55" stop-color="#b53a05"/><stop offset="1" stop-color="#fb8808"/></linearGradient>
<linearGradient id="bar-crash" x1="0" x2="1" y1="0" y2="0"><stop offset="0" stop-color="#2a0000"/><stop offset=".55" stop-color="#8a0d10"/><stop offset="1" stop-color="#e0301a"/></linearGradient>
<linearGradient id="bar-grey" x1="0" x2="1" y1="0" y2="0"><stop offset="0" stop-color="#1a1c1d"/><stop offset=".55" stop-color="#4a4f52"/><stop offset="1" stop-color="#7a8084"/></linearGradient>
<linearGradient id="metal" x1="0" x2="0" y1="0" y2="1"><stop offset="0" stop-color="#34383a"/><stop offset="1" stop-color="#24282a"/></linearGradient>
<radialGradient id="glowbg" cx=".5" cy="0" r=".7"><stop offset="0" stop-color="#b03a05" stop-opacity=".35"/><stop offset="1" stop-color="#b03a05" stop-opacity="0"/></radialGradient>
<pattern id="dots" width="6" height="6" patternUnits="userSpaceOnUse"><circle cx="3" cy="3" r=".6" fill="#fff" opacity=".07"/></pattern>
<pattern id="hazard" width="16" height="16" patternUnits="userSpaceOnUse" patternTransform="rotate(-45)"><rect width="16" height="16" fill="#050608"/><rect width="8" height="16" fill="#fb8808"/></pattern>
<filter id="glow" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="2.2"/></filter>
<clipPath id="stage"><rect x="0" y="72" width="{W}" height="{H-72}"/></clipPath>
</defs>
<rect width="{W}" height="{H}" fill="#0d0a09"/>
<rect width="{W}" height="{H}" fill="url(#glowbg)"/>
<rect width="{W}" height="{H}" fill="url(#dots)"/>

<!-- label plate -->
<rect x="14" y="12" width="{W-28}" height="44" fill="#000"/>
{bevel(14, 12, W-28, 44, 3, raised=False)}
{plate_label("label-before " + label_before, "BEFORE", "#ff5a3d")}
{plate_label(label_after, "WITH PROT", "#5af451")}
<rect x="14" y="62" width="{W-28}" height="8" fill="url(#hazard)"/>
<rect x="12" y="60" width="{W-24}" height="12" fill="none" stroke="#000" stroke-width="2"/>

<g clip-path="url(#stage)">
<g class="scene-before {before_vis}">{before}</g>
<g class="{after_vis}">{after}</g>
<g class="wipe {wipe}"><rect x="-150" y="72" width="140" height="{H-72}" fill="url(#hazard)"/><rect x="-150" y="72" width="140" height="{H-72}" fill="none" stroke="#000" stroke-width="4"/></g>
</g>

<rect x="14" y="{H-12}" width="{W-28}" height="6" fill="url(#hazard)"/>
<rect x="2" y="2" width="{W-4}" height="{H-4}" fill="none" stroke="#000" stroke-width="4"/>
</svg>
"""
OUT.write_text(svg)
print(f"wrote {OUT} ({len(svg)/1024:.0f} KiB)")
