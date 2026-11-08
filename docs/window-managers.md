# Window managers

The desktop app is a small portrait window (440x720 by default, resizable between 380x560 and
520x900). Wayland has no "float" flag: tiling compositors decide from the window's size hints and
from user rules.

- A window with a parent (a dialog) or a fixed size (min size equals max size) is floated
  automatically by niri and most other tiling WMs.
- prot stays resizable on purpose, so it is not fixed size and tiling WMs will tile it.
- To float it, add a rule that matches the app id (Wayland) or window class (X11): `prot-desktop`.

## niri

```kdl
window-rule {
    match app-id="prot-desktop"
    open-floating true
    default-column-width { fixed 440; }
    default-window-height { fixed 720; }
}
```

## sway

```
for_window [app_id="prot-desktop"] floating enable
```

## i3 (X11)

```
for_window [class="prot-desktop"] floating enable
```

## Hyprland

Rule syntax changed between releases, so check the
[window rules page](https://wiki.hypr.land/Configuring/Basics/Window-Rules/) for your version.
On older releases:

```
windowrule = float, class:^(prot-desktop)$
```

## GNOME, KDE, and other floating desktops

Windows float by default. The size limits come from `apps/desktop/src-tauri/tauri.conf.json`
(`minWidth`, `minHeight`, `maxWidth`, `maxHeight`).
