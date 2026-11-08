# Desktop design system (v2)

The desktop app is styled as Doom meets Office 2000: beveled metal panels, glossy buttons,
WordArt headings, LED status lights and clip-art starbursts. Source of truth is
`apps/desktop/src/App.css`.

## Principles

- Light comes from the top-left. Raised parts have a light top-left edge and a dark bottom-right
  edge. Pressed and sunken parts flip them.
- No border radius, except on LEDs.
- Orange means act, green means alive, gold marks numbers, red means crashed.
- Motion is limited to blinking LEDs and respects `prefers-reduced-motion`.
- The gargoyle mascot is for branding only and never appears in the app UI.

## Type

| Role | Font | Use |
| :--- | :--- | :--- |
| WordArt | Anton (bundled, Impact-like) | Page titles, nav labels, counters, ports |
| UI | Tahoma, Verdana, DejaVu Sans | Body text, buttons, card names |
| Mono | JetBrains Mono (bundled) | Commands, logs, inputs, status text |

## Colour

| Token | Value | Use |
| :--- | :--- | :--- |
| `--hazard` | `#fb8808` | Primary actions, title bars |
| `--daemon` | `#5af451` | Running, success, input text |
| `--gold` | `#ffd21a` | Numbers, ports, starburst |
| `--blood` | `#c4181b` | Crashed and error title bars |
| `--metal*` | `#9aa092` to `#0a0b0c` | Bevels and panels |

## Components

- **Title bar:** orange-to-black gradient on cards, dialogs and the app header. Crashed cards use red.
- **Buttons:** 2px bevel, glossy two-tone gradient. Primary is orange, go is green, danger turns red on hover.
- **Inputs:** black sunken well, green phosphor text, green outline on focus.
- **Status:** LED plus mono label in a sunken pill. Starting and crashed LEDs blink.
- **Bottom nav:** Doom-style status bar with a gold counter above each label.
- **Dialogs:** full-screen sheets with a title bar. Errors appear in a message box with a warning triangle.
