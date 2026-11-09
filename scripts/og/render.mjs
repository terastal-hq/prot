// Renders scripts/og/og.html to site/assets/img/og.png (1200x630), the image link previews show.
// Needs Pillow for python3 and playwright (npm i playwright && npx playwright install chromium); run it from a folder that has it:
//   node /path/to/scripts/og/render.mjs
import { createRequire } from "node:module";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

// playwright is resolved from the folder you run this in, so it does not have to be installed in the repository
const { chromium } = createRequire(path.join(process.cwd(), "noop.js"))("playwright");
const here = path.dirname(fileURLToPath(import.meta.url));
const out = path.resolve(here, "../../site/assets/img/og.png");
const b = await chromium.launch();
const page = await (await b.newContext({ viewport: { width: 1200, height: 630 } })).newPage();
await page.goto("file://" + path.join(here, "og.html"));
await page.evaluate(() => document.fonts.ready);
await page.waitForTimeout(600);
await page.screenshot({ path: out });
await b.close();
// 256 colours with dithering looks the same and is a third of the size (some apps cap previews near 600 KB)
execFileSync("python3", ["-c", `
from PIL import Image
im = Image.open(${JSON.stringify(out)}).convert("RGB")
im.quantize(256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.FLOYDSTEINBERG).save(${JSON.stringify(out)}, optimize=True)
`]);
console.log("wrote", out);
