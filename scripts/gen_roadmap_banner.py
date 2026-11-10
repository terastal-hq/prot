#!/usr/bin/env python3
"""Render the README's roadmap banner as a prot-themed SVG: an Office 2000 style window with a title bar, two
sunken "bays" for the planned features and a glossy button. The README wraps it in a link to docs/roadmap.md.

Anton and JetBrains Mono are subset to the glyphs used and embedded as data URIs, so it renders the same on GitHub.

Needs: pip install fonttools brotli
Usage: scripts/gen_roadmap_banner.py
"""
import base64
import io
import pathlib

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "docs/assets/roadmap.svg"
FONTS = ROOT / "apps/desktop/src/assets/fonts"

W, H = 860, 234
FACE_W, FACE_H = W - 6, H - 6  # 6px left over for the drop shadow

BAYS = [
    ("LOCAL HTTPS", "Serve .localhost over HTTPS with a local CA your machine trusts.", "prot tls enable | trust | status"),
    ("PUBLIC EXPOSURE", "Share one service or receive webhooks, with expiry and access control.", "prot expose web"),
]
TITLE = "NEXT UP"
NOTE = "Neither is implemented yet. The design, the limits and the open questions:"
BUTTON = "READ THE PLAN"
TAG = "PLANNED"


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;")


def embed(font: str, chars: str) -> str:
    opts = subset.Options()
    opts.flavor = "woff2"
    opts.layout_features = ["kern"]
    f = TTFont(str(FONTS / font))
    s = subset.Subsetter(opts)
    s.populate(text=chars)
    s.subset(f)
    f.flavor = "woff2"
    buf = io.BytesIO()
    f.save(buf)
    return base64.b64encode(buf.getvalue()).decode()


def bevel(x: float, y: float, w: float, h: float, light: str, dark: str, t: float = 3) -> str:
    """A raised border, light on the top and left edges, dark on the bottom and right."""
    return (
        f'<polygon points="{x},{y} {x+w},{y} {x+w-t},{y+t} {x+t},{y+t} {x+t},{y+h-t} {x},{y+h}" fill="{light}"/>'
        f'<polygon points="{x+w},{y+h} {x},{y+h} {x+t},{y+h-t} {x+w-t},{y+h-t} {x+w-t},{y+t} {x+w},{y}" fill="{dark}"/>'
    )


def well(x: float, y: float, w: float, h: float) -> str:
    """A sunken black well: the bevel is reversed, dark on top and left."""
    return f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="#050608"/>' + bevel(x, y, w, h, "#0b0c0e", "#6e747b", 2)


def render() -> str:
    glyphs = TITLE + NOTE + BUTTON + TAG + "".join(a + b for a, b, _ in BAYS)
    anton = embed("anton.woff2", glyphs)
    mono = embed("jetbrains-mono.woff2", "".join(b + c for _, b, c in BAYS) + NOTE + TAG + "0123456789.:/|-")

    parts: list[str] = []
    # drop shadow, face, raised metal frame
    parts.append(f'<rect x="5" y="5" width="{FACE_W}" height="{FACE_H}" fill="#000" opacity=".55"/>')
    parts.append(f'<rect width="{FACE_W}" height="{FACE_H}" fill="#2a2e33"/>')
    parts.append(bevel(0, 0, FACE_W, FACE_H, "#9aa0a6", "#101214", 3))

    # title bar: dark to bright orange, like the desktop app's sheet header, with three window buttons
    bar_x, bar_y, bar_w, bar_h = 6, 6, FACE_W - 12, 30
    parts.append(f'<rect x="{bar_x}" y="{bar_y}" width="{bar_w}" height="{bar_h}" fill="url(#bar)"/>')
    parts.append(f'<text class="t" x="{bar_x+12}" y="{bar_y+23}" fill="#000" opacity=".6">{TITLE}</text>')
    parts.append(f'<text class="t" x="{bar_x+11}" y="{bar_y+22}" fill="#fff">{TITLE}</text>')
    for i, glyph in enumerate(("min", "max", "close")):
        bx = bar_x + bar_w - 8 - (3 - i) * 22
        parts.append(f'<rect x="{bx}" y="{bar_y+6}" width="18" height="18" fill="#b4b9be"/>')
        parts.append(bevel(bx, bar_y + 6, 18, 18, "#fff", "#2b2f33", 2))
        if glyph == "min":
            parts.append(f'<rect x="{bx+5}" y="{bar_y+17}" width="8" height="2" fill="#000"/>')
        elif glyph == "max":
            parts.append(f'<rect x="{bx+5}" y="{bar_y+10}" width="8" height="8" fill="none" stroke="#000" stroke-width="2"/>')
        else:
            parts.append(f'<path d="M{bx+5} {bar_y+10} l8 8 M{bx+13} {bar_y+10} l-8 8" stroke="#000" stroke-width="2"/>')

    # the two bays
    y = bar_y + bar_h + 12
    bay_x, bay_w, bay_h = 14, FACE_W - 28, 54
    for name, line1, line2 in BAYS:
        parts.append(well(bay_x, y, bay_w, bay_h))
        # amber LED: dim halo, bright core, small highlight
        cx, cy = bay_x + 22, y + bay_h / 2
        parts.append(f'<circle cx="{cx}" cy="{cy}" r="9" fill="#fb8808" opacity=".25"/>')
        parts.append(f'<circle cx="{cx}" cy="{cy}" r="5.5" fill="#ffb02e" stroke="#7a3a00" stroke-width="1.5"/>')
        parts.append(f'<circle cx="{cx-1.5}" cy="{cy-1.8}" r="1.6" fill="#fff" opacity=".8"/>')
        parts.append(f'<text class="n" x="{bay_x+42}" y="{y+bay_h/2+7}" fill="#000">{esc(name)}</text>')
        parts.append(f'<text class="n" x="{bay_x+41}" y="{y+bay_h/2+6}" fill="#ffd21a">{esc(name)}</text>')
        parts.append(f'<text class="m" x="{bay_x+232}" y="{y+23}" fill="#5af451">{esc(line1)}</text>')
        parts.append(f'<text class="m" x="{bay_x+232}" y="{y+42}" fill="#8a948a">{esc(line2)}</text>')
        # "planned" tag: outlined, not filled, so it reads as a status and not a button
        tw = 72
        tx = bay_x + bay_w - tw - 10
        parts.append(f'<rect x="{tx}" y="{y+bay_h/2-10}" width="{tw}" height="20" fill="none" stroke="#fb8808" stroke-width="1.5"/>')
        parts.append(f'<text class="m" x="{tx+tw/2}" y="{y+bay_h/2+4}" fill="#fb8808" text-anchor="middle">{TAG}</text>')
        y += bay_h + 8

    # footer: the honest note, then the button
    parts.append(f'<text class="m" x="{bay_x+2}" y="{y+27}" fill="#c9ced3">{esc(NOTE)}</text>')
    bw, bh = 188, 36
    bx, by = FACE_W - bw - 16, y + 6
    parts.append(f'<rect x="{bx+4}" y="{by+4}" width="{bw}" height="{bh}" fill="#000" opacity=".55"/>')
    parts.append(f'<rect x="{bx}" y="{by}" width="{bw}" height="{bh}" fill="#050608"/>')
    parts.append(bevel(bx, by, bw, bh, "#ffe0a8", "#7a3a00", 3))
    parts.append(f'<rect x="{bx+3}" y="{by+3}" width="{bw-6}" height="{bh-6}" fill="url(#gloss)"/>')
    parts.append(f'<text class="t" x="{bx+bw/2}" y="{by+bh/2+8}" fill="#fff" opacity=".5" text-anchor="middle">{BUTTON}</text>')
    parts.append(f'<text class="t" x="{bx+bw/2}" y="{by+bh/2+7}" fill="#1a0a00" text-anchor="middle">{BUTTON}</text>')
    parts.append(f'<path d="M{bx+bw-26} {by+bh/2-6} l8 6 l-8 6 z" fill="#1a0a00"/>')

    body = "\n".join(parts)
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-label="Roadmap: local HTTPS and public exposure are planned, not implemented. Read the plan.">
<title>Roadmap</title>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{anton}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:100 800;src:url(data:font/woff2;base64,{mono}) format('woff2');}}
.t{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:20px;letter-spacing:1.5px}}
.n{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:22px;letter-spacing:1px}}
.m{{font-family:'JetBrains Mono',ui-monospace,monospace;font-weight:700;font-size:11px}}
</style>
<linearGradient id="bar" x1="0" y1="0" x2="1" y2="0"><stop offset="0" stop-color="#7a2a00"/><stop offset="1" stop-color="#fb8808"/></linearGradient>
<linearGradient id="gloss" x1="0" y1="0" x2="0" y2="1">
<stop offset="0" stop-color="#ffc15a"/><stop offset=".48" stop-color="#fb8808"/><stop offset=".52" stop-color="#d86800"/><stop offset="1" stop-color="#f58a10"/>
</linearGradient>
</defs>
{body}
</svg>
"""


def main() -> None:
    OUT.write_text(render())
    print("wrote", OUT.relative_to(ROOT), f"({OUT.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
