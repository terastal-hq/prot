#!/usr/bin/env python3
"""Render the README's install buttons as prot-themed SVGs (orange gloss, beveled, with the platform's logo).

Writes one SVG per platform to docs/assets/buttons/. Anton and JetBrains Mono are subset to the glyphs used and
embedded as data URIs, so the files render the same on GitHub. The logos are from Simple Icons (CC0).

Needs: pip install fonttools brotli. The logo paths are in scripts/install_button_icons.json (from the simple-icons
npm package, CC0).

Usage: scripts/gen_install_buttons.py
"""
import base64
import io
import json
import pathlib

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "docs/assets/buttons"
FONTS = ROOT / "apps/desktop/src/assets/fonts"
ICONS = json.loads((ROOT / "scripts/install_button_icons.json").read_text())

W, H = 244, 64

BUTTONS = [
    ("arch", "siArchlinux", "ARCH LINUX", "AUR PACKAGES"),
    ("debian", "siDebian", "DEBIAN", "UBUNTU \u00b7 .DEB \u00b7 AMD64"),
    ("fedora", "siFedora", "FEDORA", "RHEL \u00b7 .RPM \u00b7 AMD64"),
    ("macos", "siApple", "MACOS", "APPLE SILICON \u00b7 .DMG"),
]


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


def render(key: str, icon: str, title: str, sub: str) -> str:
    anton = embed("anton.woff2", title)
    mono = embed("jetbrains-mono.woff2", sub)
    path = ICONS[icon]["path"]
    w, h = W - 6, H - 6  # the face; 6px left over for the drop shadow
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-label="{title.title()}: {sub.title()}">
<title>{title.title()}</title>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{anton}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:100 800;src:url(data:font/woff2;base64,{mono}) format('woff2');}}
.t{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:26px;letter-spacing:2px}}
.s{{font-family:'JetBrains Mono',ui-monospace,monospace;font-weight:700;font-size:10px;letter-spacing:.6px}}
</style>
<linearGradient id="gloss" x1="0" y1="0" x2="0" y2="1">
<stop offset="0" stop-color="#ffc15a"/><stop offset=".48" stop-color="#fb8808"/><stop offset=".52" stop-color="#d86800"/><stop offset="1" stop-color="#f58a10"/>
</linearGradient>
</defs>
<rect x="5" y="5" width="{w}" height="{h}" fill="#000" opacity=".55"/>
<rect x="0" y="0" width="{w}" height="{h}" fill="#050608"/>
<polygon points="0,0 {w},0 {w-3},3 3,3 3,{h-3} 0,{h}" fill="#ffe0a8"/>
<polygon points="{w},{h} 0,{h} 3,{h-3} {w-3},{h-3} {w-3},3 {w},0" fill="#7a3a00"/>
<rect x="3" y="3" width="{w-6}" height="{h-6}" fill="url(#gloss)"/>
<g transform="translate(17 {(h-34)/2}) scale({34/24})" fill="#1a0a00"><path d="{path}"/></g>
<text class="t" x="64" y="{h/2+1}" fill="#fff" opacity=".5">{title}</text>
<text class="t" x="64" y="{h/2}" fill="#1a0a00">{title}</text>
<text class="s" x="64" y="{h/2+17}" fill="#1a0a00" opacity=".78">{sub}</text>
</svg>
"""


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    for key, icon, title, sub in BUTTONS:
        (OUT / f"{key}.svg").write_text(render(key, icon, title, sub))
        print("wrote", (OUT / f"{key}.svg").relative_to(ROOT))


if __name__ == "__main__":
    main()
