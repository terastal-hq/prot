#!/usr/bin/env python3
"""Render README section headings as prot-themed WordArt SVGs.

One SVG per section heading in README.md (an <img> from docs/assets/headings/, or a "## " line), written there.
Anton is subset to the heading's own glyphs and embedded as a data URI, so
each file is only a few KB and renders identically on GitHub.

Requires: pip install fonttools brotli
"""
import base64
import io
import pathlib
import re

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = pathlib.Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
OUT_DIR = ROOT / "docs/assets/headings"
FONT = ROOT / "apps/desktop/src/assets/fonts/anton.woff2"

W, H = 860, 68
SIZE = 36
EXTRUDE = 4


def slug(text: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")


def subset_font(chars: str) -> str:
    opts = subset.Options()
    opts.flavor = "woff2"
    opts.layout_features = ["kern"]
    font = TTFont(str(FONT))
    sub = subset.Subsetter(opts)
    sub.populate(text=chars)
    sub.subset(font)
    font.flavor = "woff2"
    buf = io.BytesIO()
    font.save(buf)
    return base64.b64encode(buf.getvalue()).decode()


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;")


def render(title: str) -> str:
    text = esc(title.upper())
    data = subset_font(title.upper())
    # extrusion: stacked black copies stepping down-left, then the outlined gradient face
    stack = "".join(
        f'<use href="#t" x="{-i}" y="{i}" fill="#000" stroke="#000" stroke-width="3" stroke-linejoin="round"/>'
        for i in range(EXTRUDE, 0, -1)
    )
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-label="{esc(title)}">
<title>{esc(title)}</title>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{data}) format('woff2');}}
.wa{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:{SIZE}px;letter-spacing:2px}}
</style>
<linearGradient id="face" gradientUnits="userSpaceOnUse" x1="0" y1="-31" x2="0" y2="0">
<stop offset="0" stop-color="#ffd21a"/><stop offset=".45" stop-color="#fb8808"/><stop offset=".55" stop-color="#ff5a00"/><stop offset="1" stop-color="#99e051"/>
</linearGradient>
<pattern id="hazard" width="16" height="16" patternUnits="userSpaceOnUse" patternTransform="rotate(-45)"><rect width="16" height="16" fill="#050608"/><rect width="8" height="16" fill="#fb8808"/></pattern>
<text id="t" class="wa" x="0" y="0">{text}</text>
</defs>
<g transform="translate(14 45) rotate(-2) skewX(-6)">
{stack}
<use href="#t" fill="url(#face)" stroke="#000" stroke-width="1.4" stroke-linejoin="round" paint-order="stroke fill"/>
</g>
<rect x="1" y="55" width="{W-2}" height="10" fill="#000"/>
<rect x="3" y="57" width="{W-6}" height="6" fill="url(#hazard)"/>
</svg>
"""


def main() -> None:
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    # The README shows each heading as an image, so read the titles from those (and from any plain "## " line).
    text = README.read_text()
    titles = re.findall(r'<img src="docs/assets/headings/[^"]+" alt="([^"]+)"', text)
    titles += re.findall(r"^## (.+)$", text, re.M)
    titles = list(dict.fromkeys(titles))
    for old in OUT_DIR.glob("*.svg"):
        old.unlink()
    for title in titles:
        path = OUT_DIR / f"{slug(title)}.svg"
        path.write_text(render(title))
        print(f"wrote {path.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
