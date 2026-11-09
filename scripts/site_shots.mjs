// Renders the built desktop frontend (apps/desktop/dist, served on :5199) against a mocked daemon API and writes 2x
// screenshots to ./out: the whole Servers tab, each card state, and the tunnel card. scripts/gen_site_assets.py
// cuts the landing page's images from them. Needs playwright (npm i playwright && npx playwright install chromium).
import { chromium } from "playwright";
const now = new Date().toISOString();
const svc = (o) => ({ runtime: "process", auto_port: false, auto_start: true, restart_policy: "on-failure", lan_exposed: false,
  created_at: now, updated_at: now, ...o });
const services = [
  svc({ id: "s1", name: "Storefront", alias: "shop", status: "running", port: 5173, pid: 884018, command: "npm run dev",
        canonical_url: "http://shop.localhost:8080", framework: "Vite" }),
  svc({ id: "s2", name: "Billing API", alias: "api", status: "running", port: 8000, pid: 884025, command: "python3 -m http.server 8000", canonical_url: "http://api.localhost:8080" }),
  svc({ id: "s3", name: "Docs", alias: "docs", status: "crashed", port: 8001, command: "false",
        canonical_url: "http://docs.localhost:8080" }),
  svc({ id: "s4", name: "Worker", alias: "worker", status: "stopped", port: 9100, command: "node", args: ["worker.js"],
        canonical_url: "http://worker.localhost:8080" }),
];
const tun = (id, alias, host, rp, lp, state, pw) => ({
  config: { id, alias, ssh_host: host, remote_host: "127.0.0.1", remote_port: rp, local_port: lp, auto_connect: true, has_password: pw },
  canonical_url: `http://${alias}.localhost:8080`, state, assigned_local_port: lp, pid: state === "connected" ? 5120 : undefined, reconnections: 0 });
const tunnels = [tun("t1", "proxmox", "192.168.0.109", 8006, 8006, "connected", true),
                 tun("t2", "staging-db", "bastion", 5432, 15432, "reconnecting", false)];
const dp = (id, name, port, fw, title, cmd) => ({ id, pid: 1000 + port, name, exe: "/usr/bin/" + name, cmdline: cmd, cwd: "~/code", username: "me",
  create_time: 1, ip: "127.0.0.1", port, family: "tcp4", is_http: true, http_status: 200, framework: fw, page_title: title, discovered_at: now });
const found = [dp("d1", "node", 3000, "Next.js", "Dashboard", "node next dev"), dp("d2", "python3", 5000, "", "Flask app", "python3 -m flask run"),
  dp("d3", "node", 4321, "Astro", "Blog", "node astro dev"), dp("d4", "ruby", 3001, "", "Rails", "ruby bin/rails s")];

let apiState = "running", tunState = "connected";
const b = await chromium.launch();
const ctx = await b.newContext({ viewport: { width: 520, height: 900 }, deviceScaleFactor: 2 });
const page = await ctx.newPage();
await page.addInitScript(() => {
  window.__TAURI__ = { core: { invoke: async (c) => (c === "get_auth_token" ? "x" : c === "get_admin_url" ? "http://127.0.0.1:4400" : null) } };
  window.__TAURI_INTERNALS__ = { metadata: { currentWindow: { label: "main" }, currentWebview: { label: "main", windowLabel: "main" } }, invoke: async () => null, transformCallback: () => 1 };
});
await page.route("http://127.0.0.1:4400/**", (r) => {
  const u = new URL(r.request().url()).pathname; let body = {};
  if (u === "/v1/health") body = { status: "ok", version: "0.1.0", pid: 1, gateway_addr: "127.0.0.1:8080", admin_addr: "127.0.0.1:4400", timestamp: now };
  else if (u === "/v1/services") body = { services: services.map((x) => (x.id === "s2" ? { ...x, status: apiState, pid: apiState === "crashed" ? undefined : x.pid } : x)) };
  else if (u === "/v1/discovery") body = { services: found };
  else if (u === "/v1/tunnels") body = { tunnels: tunnels.map((x) => (x.config.id === "t1" ? { ...x, state: tunState } : x)) };
  else if (u === "/v1/ssh/hosts") body = { hosts: ["bastion", "192.168.0.109"] };
  r.fulfill({ status: 200, contentType: "application/json", headers: { "access-control-allow-origin": "*", "access-control-allow-headers": "*" }, body: JSON.stringify(body) });
});
await page.goto("http://127.0.0.1:5199/");
await page.waitForTimeout(1500);
await page.screenshot({ path: "out/servers.png" });
const card = async (n, path) => { await page.locator(".card").nth(n).screenshot({ path }); };
for (const st of ["running", "crashed", "starting"]) {
  apiState = st; await page.reload(); await page.waitForTimeout(1300); await card(1, `out/card-${st}.png`);
}
await page.getByRole("button", { name: /TUNNELS/i }).last().click(); await page.waitForTimeout(900);
for (const st of ["connected", "reconnecting"]) {
  tunState = st; await page.reload(); await page.waitForTimeout(1000);
  await page.getByRole("button", { name: /TUNNELS/i }).last().click(); await page.waitForTimeout(900);
  await card(0, `out/tunnel-${st}.png`);
}
await b.close();
