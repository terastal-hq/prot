#!/usr/bin/env python3
"""Render docs/assets/before-after.svg: an animated before/after story in the prot design system.

Before: a pile of terminals, guessed ports and EADDRINUSE errors.
After:  one prot window with stable *.localhost names.

Everything loops on one 18 second CSS timeline (no scripts, so it also runs through GitHub's <img>). Fonts are
subset and embedded as data URIs. With prefers-reduced-motion the animation is off and only the "after" scene
is shown.

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
_n = [0]


def keyframes(stops: list[tuple[float, str]]) -> str:
    """Register @keyframes from (seconds, css) stops and return its name."""
    _n[0] += 1
    name = f"k{_n[0]}"
    body = "".join(f"{pct(t)}{{{css}}}" for t, css in stops)
    KEYFRAMES.append(f"@keyframes {name}{{{body}}}")
    return name


CSS_RULES: list[str] = []
_c = [0]


def animated(stops, ease="ease-out", extra="") -> str:
    """Return a class that plays the given keyframes once per loop."""
    _c[0] += 1
    cls = f"a{_c[0]}"
    CSS_RULES.append(f".{cls}{{animation:{keyframes(stops)} {T}s {ease} infinite;{extra}}}")
    return cls


HIDDEN = "opacity:0"


def enter(t0: float, dur: float = 0.5, frm: str = "translate(0px,0px)", ease="ease-out") -> str:
    """Hidden until t0, then slides/fades to its resting place and stays."""
    return animated(
        [
            (0, f"opacity:0;transform:{frm}"),
            (t0, f"opacity:0;transform:{frm}"),
            (t0 + dur, "opacity:1;transform:translate(0px,0px) scale(1)"),
            (T, "opacity:1;transform:translate(0px,0px) scale(1)"),
        ],
        ease,
    )


def pop(t0: float, dur: float = 0.45) -> str:
    return animated(
        [
            (0, "opacity:0;transform:scale(.2)"),
            (t0, "opacity:0;transform:scale(.2)"),
            (t0 + dur * 0.6, "opacity:1;transform:scale(1.15)"),
            (t0 + dur, "opacity:1;transform:scale(1)"),
            (T, "opacity:1;transform:scale(1)"),
        ],
        "ease-out",
        "transform-box:fill-box;transform-origin:center;",
    )


def window_vis(on: float, off: float, fade: float = 0.4) -> str:
    """Group visible between on and off (fading), hidden the rest of the loop."""
    return animated(
        [
            (0, "opacity:0"),
            (max(on - fade, 0), "opacity:0"),
            (on, "opacity:1"),
            (off, "opacity:1"),
            (off + fade, "opacity:0"),
            (T, "opacity:0"),
        ],
        "linear",
    )


def swap(t_on: float, t_off: float | None, fade: float = 0.25) -> str:
    """Opacity 0 -> 1 at t_on, back to 0 at t_off (or stays on until the end of the loop).

    Without animation (reduced motion) an element that switches off again stays hidden.
    """
    stops = [(0, "opacity:0"), (max(t_on - fade, 0), "opacity:0"), (t_on, "opacity:1")]
    if t_off is None:
        stops.append((T, "opacity:1"))
    else:
        stops += [(t_off, "opacity:1"), (t_off + fade, "opacity:0"), (T, "opacity:0")]
    return animated(stops, "linear", "opacity:0;" if t_off is not None else "")


def bevel(x, y, w, h, t=3, raised=True):
    a, b = (LIGHT, DARK) if raised else (DARK, LIGHT)
    h2 = t / 2
    return (
        f'<polyline points="{x+h2},{y+h-h2} {x+h2},{y+h2} {x+w-h2},{y+h2}" fill="none" stroke="{a}" '
        f'stroke-width="{t}"/>'
        f'<polyline points="{x+h2},{y+h-h2} {x+w-h2},{y+h-h2} {x+w-h2},{y+h2}" fill="none" stroke="{b}" '
        f'stroke-width="{t}"/>'
    )


def led(cx, cy, color="#5af451", blink=True, r=4.5):
    cls = ' class="blink"' if blink else ""
    return (
        f'<g{cls}><circle cx="{cx}" cy="{cy}" r="{r}" fill="{color}" filter="url(#glow)"/>'
        f'<circle cx="{cx}" cy="{cy}" r="{r-0.5}" fill="{color}"/>'
        f'<circle cx="{cx-1.5}" cy="{cy-1.5}" r="1.4" fill="#fff" opacity="0.9"/></g>'
    )


def check(x, y, color="#5af451", s=1.0):
    pts = f"{x},{y+5*s} {x+4*s},{y+9*s} {x+11*s},{y}"
    return f'<polyline points="{pts}" fill="none" stroke="{color}" stroke-width="2.6" stroke-linecap="square"/>'


def window(x, y, w, h, title, bar="url(#bar)", title_cls="wa", shadow=True):
    o = []
    if shadow:
        o.append(f'<rect x="{x-5}" y="{y+5}" width="{w}" height="{h}" fill="#000" opacity="0.6"/>')
    o.append(f'<rect x="{x-2}" y="{y-2}" width="{w+4}" height="{h+4}" fill="{INK}"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="url(#metal)"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="26" fill="{bar}"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="1" fill="#fff" opacity="0.35"/>')
    o.append(bevel(x, y, w, h))
    o.append(
        f'<text x="{x+11}" y="{y+20}" class="{title_cls}" fill="#000">{esc(title)}</text>'
        f'<text x="{x+10}" y="{y+19}" class="{title_cls}" fill="#fff">{esc(title)}</text>'
    )
    return "".join(o)


def well(x, y, w, h):
    return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#000"/>' + bevel(x, y, w, h, 3, raised=False)


def typed(x, y, w, h, t0, dur, chars):
    """A black cover over the text that shrinks to the right, revealing it left to right."""
    cls = animated(
        [(0, "transform:scaleX(1)"), (t0, "transform:scaleX(1)"), (t0 + dur, "transform:scaleX(0)"), (T, "transform:scaleX(0)")],
        f"steps({chars},end)",
        "transform:scaleX(0);transform-box:fill-box;transform-origin:right center;",
    )
    return f'<rect class="cover {cls}" x="{x}" y="{y}" width="{w}" height="{h}" fill="#000"/>'


def starburst(cx, cy, r_out, r_in, points=14):
    import math

    pts = []
    for i in range(points * 2):
        r = r_out if i % 2 == 0 else r_in
        a = math.pi * i / points - math.pi / 2
        pts.append(f"{cx + r*math.cos(a):.1f},{cy + r*math.sin(a):.1f}")
    return " ".join(pts)


# --------------------------------------------------------------------------- scene: before
def terminal(x, y, rot, title, lines, t_in, crashed=False):
    w, h = 292, 112
    bar = "url(#bar-crash)" if crashed else "url(#bar-grey)"
    cls = enter(t_in, 0.45, "translate(-46px,0px)")
    o = [f'<g transform="rotate({rot} {x + w/2} {y + h/2})"><g class="{cls}">']
    o.append(window(x, y, w, h, title, bar=bar, title_cls="ui"))
    o.append(well(x + 8, y + 34, w - 16, h - 42))
    for i, (txt, kind) in enumerate(lines):
        cls_line = f"mono {kind}" + (" errblink" if kind == "err" else "")
        o.append(f'<text x="{x+18}" y="{y+56+i*19}" class="{cls_line}">{esc(txt)}</text>')
    o.append("</g></g>")
    return "".join(o)


def question(x, y, size, t_in, delay):
    cls = pop(t_in)
    return (
        f'<g class="{cls}"><g class="bob" style="animation-delay:{delay}s">'
        f'<text x="{x+3}" y="{y+3}" class="wa" style="font-size:{size}px" fill="#000">?</text>'
        f'<text x="{x}" y="{y}" class="wa" style="font-size:{size}px" fill="#fb8808" stroke="#000" stroke-width="1.2">?</text>'
        "</g></g>"
    )


def scene_before() -> str:
    o = []
    o.append(terminal(22, 96, -2.2, "web - npm run dev", [("$ npm run dev", "grn"), ("ready on :3000", "dim")], 0.3))
    o.append(terminal(176, 160, 1.6, "api - uvicorn", [("$ uvicorn app:app --port 8000", "grn"), ("listening on :8000", "dim")], 0.9))
    o.append(terminal(26, 236, 1.2, "docs - astro", [("$ astro dev --port 4321", "grn"), ("Local http://localhost:4321", "dim")], 1.5))
    o.append(
        terminal(
            182,
            306,
            -1.8,
            "worker - python",
            [("$ python worker.py", "grn"), ("Error: EADDRINUSE :3000", "err"), ("who has 3000?!", "dim")],
            2.1,
            crashed=True,
        )
    )

    # sticky note full of guessed ports
    cls = pop(3.6, 0.5)
    sx, sy, sw, sh = 34, 428, 236, 100
    o.append(f'<g class="{cls}"><g transform="rotate(-4 {sx+sw/2} {sy+sh/2})">')
    o.append(f'<rect x="{sx-4}" y="{sy+6}" width="{sw}" height="{sh}" fill="#000" opacity="0.55"/>')
    o.append(f'<rect x="{sx}" y="{sy}" width="{sw}" height="{sh}" fill="#f4e06d"/>')
    o.append(f'<polygon points="{sx+sw-22},{sy+sh} {sx+sw},{sy+sh-22} {sx+sw},{sy+sh}" fill="#c9b13c"/>')
    o.append(f'<rect x="{sx}" y="{sy}" width="{sw}" height="16" fill="#e2cd55"/>')
    for i, line in enumerate(["web  = 3000? 3001?", "api  = 8000?? 8080?", "docs = 4321... or 4322"]):
        o.append(f'<text x="{sx+14}" y="{sy+40+i*22}" class="mono ink">{esc(line)}</text>')
    o.append(f'<path d="M{sx+44},{sy+35} h104 M{sx+44},{sy+57} h96" stroke="#c0291b" stroke-width="2" fill="none"/>')
    o.append("</g></g>")

    o.append(question(430, 150, 58, 4.2, 0.0))
    o.append(question(448, 296, 46, 4.7, 0.5))
    o.append(question(396, 470, 64, 5.1, 0.9))
    o.append(question(330, 528, 40, 5.5, 1.3))

    cap = enter(5.6, 0.4, "translate(0px,12px)")
    o.append(
        f'<g class="{cap}"><text x="{W/2+2}" y="{H-26+2}" class="wa big" text-anchor="middle" fill="#000">WHICH PORT WAS THAT AGAIN?</text>'
        f'<text x="{W/2}" y="{H-26}" class="wa big" text-anchor="middle" fill="#ff5a3d">WHICH PORT WAS THAT AGAIN?</text></g>'
    )
    return "".join(o)


# --------------------------------------------------------------------------- scene: after
def card(y, name, port_new, t_in, port_old=None, crash_until=None):
    x, w, h = 46, 428, 84
    cls = enter(t_in, 0.45, "translate(36px,0px)")
    o = [f'<g class="{cls}">']
    o.append(f'<rect x="{x-5}" y="{y+5}" width="{w}" height="{h}" fill="#000" opacity="0.55"/>')
    o.append(f'<rect x="{x-2}" y="{y-2}" width="{w+4}" height="{h+4}" fill="{INK}"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="url(#metal)"/>')
    o.append(f'<rect x="{x}" y="{y}" width="{w}" height="24" fill="url(#bar)"/>')
    if crash_until:
        c = animated(
            [(0, "opacity:1"), (crash_until, "opacity:1"), (crash_until + 0.35, "opacity:0"), (T, "opacity:0")],
            "linear",
            "opacity:0;",
        )
        o.append(f'<rect class="{c}" x="{x}" y="{y}" width="{w}" height="24" fill="url(#bar-crash)"/>')
    o.append(bevel(x, y, w, h))
    o.append(f'<text x="{x+11}" y="{y+18}" class="ui" fill="#000">{esc(name)}</text>')
    o.append(f'<text x="{x+10}" y="{y+17}" class="ui" fill="#fff">{esc(name)}</text>')

    # status badge (sunken pill with LED)
    bx = x + w - 104
    o.append(f'<rect x="{bx}" y="{y+4}" width="94" height="16" fill="#000"/>' + bevel(bx, y + 4, 94, 16, 2, raised=False))
    run = led(bx + 12, y + 12, "#5af451")
    o_run = f'<g class="{swap_status(crash_until, True)}">{run}<text x="{bx+24}" y="{y+16}" class="mono tiny grn">RUNNING</text></g>'
    o.append(o_run)
    if crash_until:
        o.append(
            f'<g class="{swap_status(crash_until, False)}">{led(bx + 12, y + 12, "#ff3b2f")}'
            f'<text x="{bx+24}" y="{y+16}" class="mono tiny red">CRASHED</text></g>'
        )

    # body: URL + port
    o.append(well(x + 8, y + 32, w - 16, 44))
    o.append(
        f'<text x="{x+20}" y="{y+61}" class="url">http://{name.lower()}.localhost<tspan class="urlport">:8080</tspan></text>'
    )
    px = x + w - 20
    if port_old:
        old = animated([(0, "opacity:1"), (14.8, "opacity:1"), (14.86, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;")
        new = animated([(0, "opacity:0"), (14.8, "opacity:0"), (14.86, "opacity:1"), (T, "opacity:1")], "linear")
        flash = animated(
            [(0, "opacity:0"), (14.8, "opacity:0"), (14.9, "opacity:.9"), (15.6, "opacity:0"), (T, "opacity:0")],
            "ease-out",
            "opacity:0;",
        )
        o.append(f'<rect class="{flash}" x="{px-78}" y="{y+38}" width="86" height="32" fill="#ffd21a"/>')
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold {old}">:{port_old}</text>')
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold {new}">:{port_new}</text>')
    else:
        o.append(f'<text x="{px}" y="{y+62}" text-anchor="end" class="wa gold">:{port_new}</text>')
    o.append("</g>")
    return "".join(o)


def swap_status(crash_until, running: bool) -> str:
    if not crash_until:
        return "" if running else ""
    if running:
        return animated([(0, "opacity:0"), (crash_until, "opacity:0"), (crash_until + 0.35, "opacity:1"), (T, "opacity:1")], "linear")
    return animated(
        [(0, "opacity:1"), (crash_until, "opacity:1"), (crash_until + 0.35, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;"
    )


def scene_after() -> str:
    o = []
    wx, wy, ww, wh = 30, 92, 460, 328
    win = enter(8.6, 0.5, "translate(0px,26px)")
    o.append(f'<g class="{win}">')
    o.append(window(wx, wy, ww, wh, "PROT"))
    # daemon chip in the title bar
    cx = wx + ww - 150
    o.append(f'<rect x="{cx}" y="{wy+4}" width="140" height="18" fill="#000"/>' + bevel(cx, wy + 4, 140, 18, 2, raised=False))
    o.append(led(cx + 12, wy + 13, "#5af451"))
    o.append(f'<text x="{cx+26}" y="{wy+17}" class="mono tiny grn">DAEMON ACTIVE</text>')
    o.append("</g>")

    o.append(card(134, "WEB", "3000", 9.1))
    o.append(card(228, "API", "51724", 9.4, port_old="8000"))
    o.append(card(322, "DOCS", "4321", 9.7, crash_until=13.6))

    # terminal strip: crashed? one command.
    term = enter(10.8, 0.45, "translate(0px,18px)")
    tx, ty, tw, th = 30, 438, 336, 70
    o.append(f'<g class="{term}">')
    o.append(f'<rect x="{tx-5}" y="{ty+5}" width="{tw}" height="{th}" fill="#000" opacity="0.55"/>')
    o.append(f'<rect x="{tx-2}" y="{ty-2}" width="{tw+4}" height="{th+4}" fill="{INK}"/>')
    o.append(f'<rect x="{tx}" y="{ty}" width="{tw}" height="{th}" fill="url(#metal)"/>' + bevel(tx, ty, tw, th))
    o.append(well(tx + 8, ty + 8, tw - 16, th - 16))
    o.append(f'<text x="{tx+20}" y="{ty+31}" class="mono grn">$ prot restart docs</text>')
    o.append(typed(tx + 19, ty + 18, 148, 18, 11.6, 1.6, 19))
    done = swap(13.7, None, 0.2)
    o.append(
        f'<g class="{done}">{check(tx+20, ty+42)}'
        f'<text x="{tx+38}" y="{ty+52}" class="mono dim">docs.localhost:8080 is running</text></g>'
    )
    o.append("</g>")

    # starburst
    sb = pop(15.3, 0.55)
    bx, by = 428, 476
    o.append(f'<g class="{sb}">')
    o.append(f'<g class="spin"><polygon points="{starburst(bx+3, by+4, 58, 42)}" fill="#000" opacity="0.55"/>')
    o.append(f'<polygon points="{starburst(bx, by, 58, 42)}" fill="#ffd21a" stroke="#000" stroke-width="3" stroke-linejoin="round"/></g>')
    o.append(f'<text x="{bx}" y="{by-3}" text-anchor="middle" class="wa burst">NO PORT</text>')
    o.append(f'<text x="{bx}" y="{by+15}" text-anchor="middle" class="wa burst">GUESSING</text>')
    o.append("</g>")

    # bottom caption: swaps when the port changes
    cap1 = swap(10.6, 14.6, 0.2)
    cap2 = swap(15.0, None, 0.25)
    for cls, text, fill in ((cap1, "ONE PLACE FOR EVERY SERVER.", "#5af451"), (cap2, "PORTS CHANGE. NAMES DON'T.", "#ffd21a")):
        o.append(
            f'<g class="{cls}"><text x="{W/2+2}" y="{H-26+2}" class="wa big" text-anchor="middle" fill="#000">{esc(text)}</text>'
            f'<text x="{W/2}" y="{H-26}" class="wa big" text-anchor="middle" fill="{fill}">{esc(text)}</text></g>'
        )
    return "".join(o)


# --------------------------------------------------------------------------- assemble
# Without animation (reduced motion, or a renderer that ignores CSS animation) only the finished "after" scene shows.
before_vis = animated([(0, "opacity:1"), (8.0, "opacity:1"), (8.6, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;")
after_vis = animated(
    [(0, "opacity:0"), (8.3, "opacity:0"), (8.7, "opacity:1"), (17.5, "opacity:1"), (17.95, "opacity:0"), (T, "opacity:0")],
    "linear",
)
cap_before = animated(
    [(0, "opacity:0"), (0.3, "opacity:1"), (8.0, "opacity:1"), (8.4, "opacity:0"), (T, "opacity:0")], "linear", "opacity:0;"
)
cap_after = animated([(0, "opacity:0"), (8.6, "opacity:0"), (9.0, "opacity:1"), (17.5, "opacity:1"), (17.95, "opacity:0"), (T, "opacity:0")], "linear")
# The band waits just off-screen left (x=-150) and sweeps across the stage between 7.9s and 9.0s.
wipe = animated(
    [(0, "transform:translateX(0px)"), (7.9, "transform:translateX(0px)"), (9.0, "transform:translateX(700px)"), (T, "transform:translateX(700px)")],
    "ease-in-out",
)

before = scene_before()
after = scene_after()


def caption(cls, text, color):
    return (
        f'<g class="{cls}"><text x="{W/2+2}" y="45" class="wa title" text-anchor="middle" fill="#000">{esc(text)}</text>'
        f'<text x="{W/2}" y="43" class="wa title" text-anchor="middle" fill="{color}">{esc(text)}</text></g>'
    )


svg = f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-labelledby="t d">
<title id="t">prot, before and after</title>
<desc id="d">Animation. Before: four terminals, guessed port numbers on a sticky note and an address-in-use error. After: one prot window lists web, api and docs under stable localhost names, a crashed service is restarted with one command, and a port changes while its address stays the same.</desc>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{font_b64("anton.woff2")}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:400 700;src:url(data:font/woff2;base64,{font_b64("jetbrains-mono.woff2")}) format('woff2');}}
.wa{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:17px;letter-spacing:1.2px}}
.title{{font-size:25px;letter-spacing:1.6px}}
.big{{font-size:21px;letter-spacing:1.3px}}
.gold{{fill:#ffd21a;font-size:24px}}
.burst{{font-size:15px;fill:#000;letter-spacing:.8px}}
.ui{{font-family:Tahoma,Verdana,'DejaVu Sans',sans-serif;font-weight:700;font-size:13px;letter-spacing:.4px}}
.mono{{font-family:'JetBrains Mono',ui-monospace,monospace;font-size:12.5px}}
.tiny{{font-size:10.5px;font-weight:700;letter-spacing:.8px}}
.grn{{fill:#5af451}} .dim{{fill:#9aa092}} .red{{fill:#ff3b2f}} .err{{fill:#ff3b2f;font-weight:700}} .ink{{fill:#3a2a00;font-weight:700}}
.url{{font-family:Tahoma,Verdana,'DejaVu Sans',sans-serif;font-weight:700;font-size:17px;fill:#8bd13a}}
.urlport{{font-size:13px;fill:#9aa092;font-weight:400}}
.cover{{}}
.blink{{animation:blink 1.6s ease-in-out infinite}}
.errblink{{animation:errblink .9s steps(2,jump-none) infinite}}
.bob{{animation:bob 2.2s ease-in-out infinite alternate}}
.spin{{animation:spin 14s linear infinite;transform-box:fill-box;transform-origin:center}}
@keyframes blink{{0%,100%{{opacity:1}}50%{{opacity:.45}}}}
@keyframes errblink{{0%{{opacity:1}}100%{{opacity:.25}}}}
@keyframes bob{{from{{transform:translateY(-4px) rotate(-6deg)}}to{{transform:translateY(5px) rotate(6deg)}}}}
@keyframes spin{{to{{transform:rotate(360deg)}}}}
{chr(10).join(CSS_RULES)}
{chr(10).join(KEYFRAMES)}
.bob{{transform-box:fill-box;transform-origin:center}}
@media (prefers-reduced-motion: reduce){{
  *{{animation:none !important}}
  .scene-before,.cap-before,.wipe,.cover{{display:none}}
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

<!-- caption plate -->
<rect x="14" y="12" width="{W-28}" height="44" fill="#000"/>
<rect x="14" y="12" width="{W-28}" height="44" fill="none" stroke="#000" stroke-width="2"/>
{bevel(14, 12, W-28, 44, 3, raised=False)}
{caption("cap-before " + cap_before, "BEFORE: THE TERMINAL JUGGLE", "#ff5a3d")}
{caption(cap_after, "AFTER: ONE CONTROL PLANE", "#5af451")}
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
