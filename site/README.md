# Landing page

Plain HTML, CSS and a little JavaScript. No build step, no dependencies: the folder is the site.

```bash
python3 -m http.server 5200 --directory site    # then open http://127.0.0.1:5200
```

`.github/workflows/pages.yml` publishes this folder to GitHub Pages when it changes on `main`. Turn it on once under
Settings > Pages > Source: GitHub Actions.

## Where things come from

Nothing on the page is drawn to look like prot. It is prot.

- **The exploded app, the cards and the tunnel card** are cut from real 2x captures of the desktop app
  (`scripts/site_shots.mjs` renders the built frontend with a mocked daemon API; `scripts/gen_site_assets.py` cuts the
  pieces into `assets/img/`). The pieces' home rectangles in `index.html` are in the app's own pixels, so change them
  together with `PIECES` in that script.
- **Every terminal** shows output the CLI really printed. `scripts/site_capture_cli.sh` records it from a throwaway
  `protd`, and `gen_site_assets.py` writes it to `data.js`. Do not edit `data.js` by hand.
- **Fonts** are the desktop app's (Anton, JetBrains Mono). Colours and bevels are the tokens in
  `apps/desktop/src/App.css`.

## Regenerating the images

```bash
cd apps/desktop && npm run build                              # the frontend the captures are taken from
python3 -m http.server 5199 --directory apps/desktop/dist &   # scripts/site_shots.mjs expects it here
cd /tmp && npm i playwright && npx playwright install chromium    # once; run the script from a folder with playwright
node scripts/site_shots.mjs                                   # writes ./out/*.png
python3 scripts/gen_site_assets.py out /tmp/cap/out           # needs Pillow
```

`site_capture_cli.sh` refuses to run while ports 8080, 4400, 5173, 8000 or 8001 are taken, so stop your own `protd`
first.

## Behaviour

Motion is explicit and user-driven where it matters (the kill button, the plug, the PROT/UNDO button); the intro and
the first run of each demo play once when scrolled into view. With `prefers-reduced-motion` everything renders in its
final state. Without JavaScript the ports show up already named.
