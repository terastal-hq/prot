#!/usr/bin/env python3
"""Render the README's badges as prot-themed SVGs: a raised metal label next to a sunken black well with the value
in the app's number colours, like the counters in the desktop app (docs: the design system, App.css).

Edit BADGES below and run this to change a value, or edit the <text> of a generated SVG by hand: Anton and
JetBrains Mono are embedded for all printable ASCII, so any value you type renders. (Widths are fixed by this script,
so after a hand edit that changes the length, run it instead.)

Needs: pip install fonttools brotli
Usage: scripts/gen_badges.py
"""
import base64
import io
import json
import pathlib

from fontTools import subset
from fontTools.ttLib import TTFont

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "docs/assets/badges"
FONTS = ROOT / "apps/desktop/src/assets/fonts"
ICONS = json.loads((ROOT / "scripts/install_button_icons.json").read_text())

GOLD, GREEN, ORANGE = "#ffd21a", "#5af451", "#fb8808"

# file name, label, value, value colour, icon key (or None)
BADGES = [
    ("release", "prot", "v0.2.1", GOLD, None),
    ("go", "go", "1.27", GREEN, None),
    ("aur", "aur", "0.2.1-1", ORANGE, "siArchlinux"),
    ("platform", "platform", "linux | macOS", GREEN, None),
    ("license", "license", "MIT", ORANGE, None),
]

H = 24  # face height; the drop shadow adds 2
LABEL_SIZE = 10
VALUE_SIZE = 16
LABEL_SPACING = 0.9
VALUE_SPACING = 0.6
PAD = 9
ICON = 13
ASCII = "".join(chr(c) for c in range(32, 127))


def font_data(name: str) -> tuple[str, TTFont]:
    opts = subset.Options()
    opts.flavor = "woff2"
    opts.layout_features = ["kern"]
    f = TTFont(str(FONTS / name))
    s = subset.Subsetter(opts)
    s.populate(text=ASCII)
    s.subset(f)
    f.flavor = "woff2"
    buf = io.BytesIO()
    f.save(buf)
    return base64.b64encode(buf.getvalue()).decode(), TTFont(str(FONTS / name))


def text_width(font: TTFont, text: str, size: float, spacing: float) -> float:
    cmap, hmtx, upm = font.getBestCmap(), font["hmtx"], font["head"].unitsPerEm
    return sum(hmtx[cmap.get(ord(c), 0) if ord(c) in cmap else ".notdef"][0] for c in text) * size / upm + spacing * len(text)


def render(label: str, value: str, color: str, icon: str | None, anton: tuple, mono: tuple) -> str:
    label_u, value_u = label.upper(), value  # labels are small caps like the app; values keep their own case
    lw = text_width(mono[1], label_u, LABEL_SIZE, LABEL_SPACING)
    vw = text_width(anton[1], value_u, VALUE_SIZE, VALUE_SPACING)
    icon_w = ICON + 5 if icon else 0
    left = round(PAD + icon_w + lw + PAD - LABEL_SPACING)
    right = round(PAD + vw + PAD - VALUE_SPACING)
    w = left + right
    iconsvg = ""
    if icon:
        iconsvg = (f'<g transform="translate({PAD - 1} {(H - ICON) / 2}) scale({ICON / 24})" fill="{ORANGE}">'
                   f'<path d="{ICONS[icon]["path"]}"/></g>')
    tx = PAD + icon_w
    return f"""<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w + 2} {H + 2}" width="{w + 2}" height="{H + 2}" role="img" aria-label="{label}: {value}">
<title>{label}: {value}</title>
<defs>
<style>
@font-face{{font-family:'Anton';src:url(data:font/woff2;base64,{anton[0]}) format('woff2');}}
@font-face{{font-family:'JetBrains Mono';font-weight:100 800;src:url(data:font/woff2;base64,{mono[0]}) format('woff2');}}
.l{{font-family:'JetBrains Mono',ui-monospace,monospace;font-weight:700;font-size:{LABEL_SIZE}px;letter-spacing:{LABEL_SPACING}px}}
.v{{font-family:Anton,Impact,'Arial Black',sans-serif;font-size:{VALUE_SIZE}px;letter-spacing:{VALUE_SPACING}px}}
</style>
<linearGradient id="metal" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#6a7072"/><stop offset=".48" stop-color="#4a4f51"/><stop offset=".52" stop-color="#363a3c"/><stop offset="1" stop-color="#444849"/></linearGradient>
</defs>
<rect x="2" y="2" width="{w}" height="{H}" fill="#000" opacity=".5"/>
<rect x="0" y="0" width="{w}" height="{H}" fill="#0a0b0c"/>
<polygon points="0,0 {left},0 {left - 2},2 2,2 2,{H - 2} 0,{H}" fill="#9aa092"/>
<polygon points="{left},{H} 0,{H} 2,{H - 2} {left - 2},{H - 2} {left - 2},2 {left},0" fill="#0a0b0c"/>
<rect x="2" y="2" width="{left - 4}" height="{H - 4}" fill="url(#metal)"/>
<rect x="{left}" y="0" width="{right}" height="{H}" fill="#000"/>
<polygon points="{left},0 {w},0 {w - 2},2 {left + 2},2 {left + 2},{H - 2} {left},{H}" fill="#0a0b0c"/>
<polygon points="{w},{H} {left},{H} {left + 2},{H - 2} {w - 2},{H - 2} {w - 2},2 {w},0" fill="#9aa092"/>
{iconsvg}
<text class="l" x="{tx + 1}" y="{H / 2 + 4.5}" fill="#000">{label_u}</text>
<text class="l" x="{tx}" y="{H / 2 + 3.5}" fill="#f2f1ef">{label_u}</text>
<text class="v" x="{left + PAD}" y="{H / 2 + 6}" fill="{color}">{value_u}</text>
</svg>
"""


def main() -> None:
    anton, mono = font_data("anton.woff2"), font_data("jetbrains-mono.woff2")
    for name, label, value, color, icon in BADGES:
        (OUT / f"{name}.svg").write_text(render(label, value, color, icon, anton, mono))
        print("wrote", (OUT / f"{name}.svg").relative_to(ROOT))


if __name__ == "__main__":
    main()
