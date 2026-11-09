import React, { useState, useEffect, useRef, useCallback } from "react";
import { Play, Square, RotateCw, Trash2, Terminal, Pencil, Settings2, ChevronDown, Plus, Copy, Check, X } from "lucide-react";
import { client } from "./api";
import { DaemonHealth, DiscoveredProcess, LogEntry, ServiceView, TunnelStatus } from "./types";
import wordmark from "./assets/wordmark.png";
import gearsIcon from "./assets/settings-gears.png";
import "./App.css";

type Tab = "servers" | "found" | "tunnels" | "settings";
type NavTab = Exclude<Tab, "settings">;
type Kind = "run" | "busy" | "idle" | "crash";

const LABELS: Record<Kind, string> = { run: "RUNNING", busy: "STARTING", idle: "IDLE", crash: "CRASHED" };

function serviceKind(status: ServiceView["status"]): Kind {
  switch (status) {
    case "running":
      return "run";
    case "starting":
      return "busy";
    case "crashed":
    case "unhealthy":
      return "crash";
    default:
      return "idle";
  }
}

function tunnelKind(state: TunnelStatus["state"]): Kind {
  switch (state) {
    case "connected":
      return "run";
    case "connecting":
    case "reconnecting":
      return "busy";
    case "failed":
      return "crash";
    default:
      return "idle";
  }
}

/** The part of the canonical URL after the alias, e.g. ".localhost:8080". */
function urlSuffix(s: ServiceView) {
  const url = s.canonical_url || `http://${s.alias}.localhost`;
  const i = url.indexOf(s.alias);
  return i >= 0 ? url.slice(i + s.alias.length) : ".localhost";
}

type EditForm = {
  id: string;
  name: string;
  alias: string;
  command: string;
  cwd: string;
  port: string;
  autoPort: boolean;
  autoStart: boolean;
  restartPolicy: string;
  lanExposed: boolean;
  env: string;
};

const RESTART_POLICIES = [
  { value: "no", label: "Never" },
  { value: "on-failure", label: "On failure" },
  { value: "always", label: "Always" },
];

function envToText(env?: Record<string, string>) {
  return Object.entries(env ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

/** Parses KEY=value lines; throws with the offending line number on bad input. */
function textToEnv(text: string): Record<string, string> {
  const env: Record<string, string> = {};
  text.split("\n").forEach((raw, i) => {
    const line = raw.trim();
    if (!line || line.startsWith("#")) return;
    const eq = line.indexOf("=");
    if (eq < 1) throw new Error(`Environment line ${i + 1} must look like KEY=value`);
    env[line.slice(0, eq).trim()] = line.slice(eq + 1);
  });
  return env;
}

function toEditForm(s: ServiceView): EditForm {
  return {
    id: s.id,
    name: s.name,
    alias: s.alias,
    command: s.command ?? "",
    cwd: s.cwd ?? "",
    port: s.port > 0 ? String(s.port) : "",
    autoPort: s.auto_port,
    autoStart: s.auto_start,
    restartPolicy: s.restart_policy || "on-failure",
    lanExposed: s.lan_exposed,
    env: envToText(s.env),
  };
}

/** Splits "http://a.localhost:8080" into ["http://a.localhost", ":8080"] so the port can be de-emphasised. */
function splitUrlPort(url: string): string[] {
  const m = url.match(/^(.*?)(:\d+)$/);
  return m ? [m[1], m[2]] : [url];
}

function GearIcon() {
  return <img src={gearsIcon} alt="" width={40} height={35} draggable={false} />;
}

function Status({ kind, text }: { kind: Kind; text?: string }) {
  const label = text ?? LABELS[kind];
  if (kind === "crash") {
    return (
      <span className="status crash">
        <span className="stripe" aria-hidden="true" />
        <span className="txt">{label}</span>
      </span>
    );
  }
  return (
    <span className={`status ${kind}`}>
      <span className="sq" aria-hidden="true" />
      {label}
    </span>
  );
}

function Field(props: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  prompt?: string;
  type?: string;
  required?: boolean;
  autoFocus?: boolean;
}) {
  return (
    <div className="field">
      <label>{props.label}</label>
      <div className="input-well">
        <span className="prompt" aria-hidden="true">
          {props.prompt ?? ">"}
        </span>
        <input
          type={props.type ?? "text"}
          required={props.required}
          autoFocus={props.autoFocus}
          placeholder={props.placeholder}
          value={props.value}
          onChange={(e) => props.onChange(e.target.value)}
          spellCheck={false}
        />
      </div>
    </div>
  );
}

/** Themed dropdown; the native <select> popup can't be styled in the webview. */
function Select(props: {
  id: string;
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (v: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const current = props.options.find((o) => o.value === props.value);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);

  const step = (dir: number) => {
    const i = props.options.findIndex((o) => o.value === props.value);
    const next = props.options[Math.min(Math.max(i + dir, 0), props.options.length - 1)];
    props.onChange(next.value);
  };

  return (
    <div className="field select" ref={root}>
      <label id={`${props.id}-label`}>{props.label}</label>
      <button
        type="button"
        id={props.id}
        className="select-btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby={`${props.id}-label ${props.id}`}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key === "Escape" && open) {
            e.stopPropagation();
            setOpen(false);
          } else if (e.key === "ArrowDown") {
            e.preventDefault();
            step(1);
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            step(-1);
          }
        }}
      >
        <span>{current?.label ?? props.value}</span>
        <ChevronDown size={14} strokeWidth={3} />
      </button>
      {open && (
        <ul className="select-list" role="listbox" aria-labelledby={`${props.id}-label`}>
          {props.options.map((o) => (
            <li
              key={o.value}
              role="option"
              aria-selected={o.value === props.value}
              className={o.value === props.value ? "on" : ""}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => {
                props.onChange(o.value);
                setOpen(false);
              }}
            >
              {o.label}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function App() {
  const [tab, setTabState] = useState<Tab>("servers");
  const lastTab = useRef<NavTab>("servers");
  const setTab = (t: Tab) => {
    if (t !== "settings") lastTab.current = t;
    setTabState(t);
  };
  const [query, setQuery] = useState("");
  const [health, setHealth] = useState<DaemonHealth | null>(null);
  const [isOnline, setIsOnline] = useState(false);
  const [services, setServices] = useState<ServiceView[]>([]);
  const [discovered, setDiscovered] = useState<DiscoveredProcess[]>([]);
  const [tunnels, setTunnels] = useState<TunnelStatus[]>([]);

  const [toast, setToast] = useState<{ text: string; ok: boolean } | null>(null);
  const toastTimer = useRef<number | undefined>(undefined);

  // Logs sheet
  const [logService, setLogService] = useState<ServiceView | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const logEnd = useRef<HTMLDivElement>(null);

  // New service sheet
  const [showAdd, setShowAdd] = useState(false);
  const [svcName, setSvcName] = useState("");
  const [svcCmd, setSvcCmd] = useState("");
  const [svcAlias, setSvcAlias] = useState("");
  const [svcPort, setSvcPort] = useState("");
  const [svcCwd, setSvcCwd] = useState("");
  const [svcAutoPort, setSvcAutoPort] = useState(false);

  // Adopt sheet
  const [adopting, setAdopting] = useState<DiscoveredProcess | null>(null);
  const [adoptAlias, setAdoptAlias] = useState("");

  // Delete confirmation (two-step button)
  const [armedDelete, setArmedDelete] = useState<string | null>(null);

  // Settings
  const [adminUrl, setAdminUrl] = useState(client.getBaseUrl());
  const [authToken, setAuthToken] = useState(client.getToken());
  const [editingAlias, setEditingAlias] = useState<{ id: string; value: string } | null>(null);
  const [editForm, setEditForm] = useState<EditForm | null>(null);
  const [pendingRestart, setPendingRestart] = useState<Set<string>>(new Set());
  const [copiedMCP, setCopiedMCP] = useState(false);

  const notify = useCallback((text: string, ok = false) => {
    setToast({ text, ok });
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(null), 4000);
  }, []);

  const fail = useCallback((err: unknown) => notify(err instanceof Error ? err.message : String(err)), [notify]);

  const refreshData = useCallback(async () => {
    try {
      setHealth(await client.getHealth());
      setIsOnline(true);
      setServices(await client.listServices());
      setDiscovered(await client.listDiscovery());
      setTunnels(await client.listTunnels());
    } catch {
      setIsOnline(false);
    }
  }, []);

  useEffect(() => {
    const init = async () => {
      await client.init();
      setAuthToken(client.getToken());
      setAdminUrl(client.getBaseUrl());
      await refreshData();
    };
    init();
    const interval = setInterval(refreshData, 3000);
    return () => clearInterval(interval);
  }, [refreshData]);

  useEffect(() => {
    if (!logService) return;
    const fetchLogs = async () => {
      try {
        setLogs(await client.getLogs(logService.id, 100));
      } catch (err) {
        console.error(err);
      }
    };
    fetchLogs();
    const interval = setInterval(fetchLogs, 1500);
    return () => clearInterval(interval);
  }, [logService]);

  useEffect(() => {
    logEnd.current?.scrollIntoView({ block: "end" });
  }, [logs]);

  const run = async (action: () => Promise<unknown>) => {
    try {
      await action();
      await refreshData();
    } catch (err) {
      fail(err);
    }
  };

  const saveAlias = async (s: ServiceView) => {
    if (!editingAlias) return;
    const alias = editingAlias.value.trim().toLowerCase();
    if (alias === s.alias) {
      setEditingAlias(null);
      return;
    }
    try {
      await client.renameAlias(s.id, alias);
      setEditingAlias(null);
      await refreshData();
      notify(`Now at ${alias}.localhost`, true);
    } catch (err) {
      fail(err);
    }
  };

  const clearPending = (id: string) =>
    setPendingRestart((cur) => {
      if (!cur.has(id)) return cur;
      const next = new Set(cur);
      next.delete(id);
      return next;
    });

  const runLifecycle = (id: string, action: () => Promise<unknown>) => {
    clearPending(id);
    return run(action);
  };

  const handleEditSave = async (e: React.FormEvent) => {
    e.preventDefault();
    const f = editForm;
    const orig = services.find((x) => x.id === f?.id);
    if (!f || !orig) return;
    try {
      const env = textToEnv(f.env);
      const port = f.port.trim() === "" ? 0 : Number(f.port);
      if (!Number.isInteger(port) || port < 0 || port > 65535) throw new Error("Port must be 0-65535");
      // With auto-port the shown port is the live ephemeral one, so only send it if the user edited it.
      const portChanged = f.port !== toEditForm(orig).port;
      await client.updateService(f.id, {
        name: f.name,
        alias: f.alias,
        command: f.command,
        cwd: f.cwd,
        env,
        ...(portChanged ? { port } : {}),
        auto_port: f.autoPort,
        auto_start: f.autoStart,
        restart_policy: f.restartPolicy,
        lan_exposed: f.lanExposed,
      });
      const live = orig.status === "running" || orig.status === "starting" || orig.status === "unhealthy";
      const needsRestart =
        live &&
        (f.command.trim() !== (orig.command ?? "") ||
          f.cwd.trim() !== (orig.cwd ?? "") ||
          envToText(env) !== envToText(orig.env) ||
          portChanged ||
          f.autoPort !== orig.auto_port ||
          f.restartPolicy !== orig.restart_policy);
      if (needsRestart) setPendingRestart((cur) => new Set(cur).add(f.id));
      setEditForm(null);
      await refreshData();
      notify(needsRestart ? "Saved. Restart to apply." : "Saved", true);
    } catch (err) {
      fail(err);
    }
  };

  const handleDelete = (id: string) => {
    if (armedDelete !== id) {
      setArmedDelete(id);
      window.setTimeout(() => setArmedDelete((cur) => (cur === id ? null : cur)), 3000);
      return;
    }
    setArmedDelete(null);
    run(() => client.deleteService(id));
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await client.createService({
        name: svcName,
        command: svcCmd,
        alias: svcAlias || svcName.toLowerCase().replace(/\s+/g, "-"),
        port: svcPort ? parseInt(svcPort, 10) : 0,
        cwd: svcCwd,
        auto_port: svcAutoPort,
        restart_policy: "on-failure",
      });
      setShowAdd(false);
      setSvcName("");
      setSvcCmd("");
      setSvcAlias("");
      setSvcPort("");
      setSvcCwd("");
      setSvcAutoPort(false);
      await refreshData();
    } catch (err) {
      fail(err);
    }
  };

  const openAdopt = (d: DiscoveredProcess) => {
    setAdopting(d);
    setAdoptAlias((d.name || `app-${d.port}`).toLowerCase().replace(/\s+/g, "-"));
  };

  const handleAdopt = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!adopting || !adoptAlias) return;
    try {
      await client.adoptDiscovery(adopting.id, {
        name: adopting.name || `Service on :${adopting.port}`,
        alias: adoptAlias,
        command: adopting.cmdline,
      });
      setAdopting(null);
      await refreshData();
      setTab("servers");
    } catch (err) {
      fail(err);
    }
  };

  const saveSettings = () => {
    client.setBaseUrl(adminUrl);
    client.setToken(authToken);
    refreshData();
    notify("Settings saved.", true);
  };

  const mcpConfigSnippet = JSON.stringify({ mcpServers: { prot: { command: "prot", args: ["mcp"] } } }, null, 2);

  const q = query.trim().toLowerCase();
  const filteredDiscovered = q
    ? discovered.filter((d) =>
        [d.name, d.exe, d.cmdline, d.framework, d.page_title, d.http_server, d.cwd, String(d.port), String(d.pid)]
          .filter(Boolean)
          .some((f) => String(f).toLowerCase().includes(q)),
      )
    : discovered;

  const running = services.filter((s) => s.status === "running").length;
  const crashed = services.filter((s) => s.status === "crashed" || s.status === "unhealthy").length;
  const idle = services.length - running - crashed;

  const daemonInfo = (
    <div className="card">
      <div className="card-head">
        <span className="label">PROTD</span>
        <Status kind={isOnline ? "run" : "crash"} text={isOnline ? "ACTIVE" : "OFFLINE"} />
      </div>
      <div className="card-line">
        <code>{health ? `v${health.version}` : "no connection"}</code>
        {health && <span className="dim">pid {health.pid}</span>}
      </div>
      {health && <span className="dim">{health.admin_addr}</span>}
    </div>
  );

  return (
    <div className="app">
      <header className="app-header">
        <img src={wordmark} alt="prot" />
        <div className="header-right">
          <span className={`daemon-chip ${isOnline ? "" : "offline"}`}>
            <span className="sq" aria-hidden="true" />
            {isOnline ? "DAEMON ACTIVE" : "DAEMON OFFLINE"}
          </span>
          <button
            className={`burst ${tab === "settings" ? "pressed" : ""}`}
            title="Settings"
            aria-label="Settings"
            aria-pressed={tab === "settings"}
            onClick={() => (tab === "settings" ? setTab(lastTab.current) : setTab("settings"))}
          >
            <GearIcon />
          </button>
        </div>
      </header>

      <main className="screen">
        {/* SERVERS */}
        {tab === "servers" && (
          <>
            <div className="screen-title">
              <h1>servers</h1>
              <button className="btn btn-primary" onClick={() => setShowAdd(true)}>
                <Plus size={16} strokeWidth={2.5} /> New
              </button>
            </div>

            {services.length > 0 && (
              <div className="summary">
                <span className="summary-chip run">
                  <b>{running}</b> RUNNING
                </span>
                {crashed > 0 && (
                  <span className="summary-chip crash">
                    <b>{crashed}</b> CRASHED
                  </span>
                )}
                <span className="summary-chip">
                  <b>{idle}</b> IDLE
                </span>
              </div>
            )}

            {services.length === 0 ? (
              <>
                <div className="empty">
                  <p>No servers yet. Register one, or adopt something already listening from the FOUND tab.</p>
                </div>
              </>
            ) : (
              services.map((s) => {
                const kind = serviceKind(s.status);
                return (
                  <div key={s.id} className={`card ${kind === "crash" ? "crashed" : ""}`}>
                    <div className="card-head">
                      <span className="card-name">{s.name}</span>
                      <Status kind={kind} />
                    </div>
                    <div className="card-line">
                      <code>&gt; {s.command || "external"}</code>
                      {s.port > 0 && <span className="port">:{s.port}</span>}
                      {s.pid ? <span className="dim">pid {s.pid}</span> : null}
                    </div>
                    {editingAlias?.id === s.id ? (
                      <form
                        className="url-edit"
                        onSubmit={(e) => {
                          e.preventDefault();
                          saveAlias(s);
                        }}
                      >
                        <span className="url-affix">http://</span>
                        <input
                          className="url-input"
                          autoFocus
                          spellCheck={false}
                          value={editingAlias.value}
                          size={Math.max(editingAlias.value.length + 1, 4)}
                          onChange={(e) => setEditingAlias({ id: s.id, value: e.target.value })}
                          onKeyDown={(e) => e.key === "Escape" && setEditingAlias(null)}
                        />
                        <span className="url-affix">{urlSuffix(s)}</span>
                        <button type="submit" className="icon-btn go" title="Save">
                          <Check size={14} strokeWidth={3} />
                        </button>
                        <button type="button" className="icon-btn" title="Cancel" onClick={() => setEditingAlias(null)}>
                          <X size={14} strokeWidth={3} />
                        </button>
                      </form>
                    ) : (
                      <div className="url-row">
                        <a
                          className="card-url"
                          href={s.canonical_url || `http://${s.alias}.localhost`}
                          target="_blank"
                          rel="noreferrer"
                        >
                          {splitUrlPort(s.canonical_url || `http://${s.alias}.localhost`).map((part, i) =>
                            <span key={i} className={i === 0 ? "url-host" : "url-port"}>{part}</span>
                          )}
                        </a>
                        <button
                          className="icon-btn"
                          title="Edit URL"
                          onClick={() => setEditingAlias({ id: s.id, value: s.alias })}
                        >
                          <Pencil size={14} strokeWidth={2.5} />
                        </button>
                      </div>
                    )}
                    {pendingRestart.has(s.id) && (
                      <div className="restart-note">
                        <span>Changes pending. Restart to apply.</span>
                        <button
                          type="button"
                          className="btn btn-primary"
                          onClick={() => runLifecycle(s.id, () => client.restartService(s.id))}
                        >
                          Restart
                        </button>
                      </div>
                    )}
                    <div className="card-actions">
                      {s.status === "running" ? (
                        <button className="icon-btn danger" title="Stop" onClick={() => runLifecycle(s.id, () => client.stopService(s.id))}>
                          <Square size={14} strokeWidth={2.5} />
                        </button>
                      ) : (
                        <button className="icon-btn go" title="Start" onClick={() => runLifecycle(s.id, () => client.startService(s.id))}>
                          <Play size={14} strokeWidth={2.5} />
                        </button>
                      )}
                      <button className="icon-btn" title="Restart" onClick={() => runLifecycle(s.id, () => client.restartService(s.id))}>
                        <RotateCw size={14} strokeWidth={2.5} />
                      </button>
                      <button className="icon-btn" title="Logs" onClick={() => setLogService(s)}>
                        <Terminal size={14} strokeWidth={2.5} />
                      </button>
                      <button className="icon-btn" title="Edit server settings" onClick={() => setEditForm(toEditForm(s))}>
                        <Settings2 size={14} strokeWidth={2.5} />
                      </button>
                      <span className="spacer" />
                      <button
                        className={`icon-btn danger ${armedDelete === s.id ? "armed" : ""}`}
                        title={armedDelete === s.id ? "Click again to remove" : "Remove"}
                        onClick={() => handleDelete(s.id)}
                      >
                        <Trash2 size={14} strokeWidth={2.5} />
                      </button>
                    </div>
                  </div>
                );
              })
            )}
          </>
        )}

        {/* FOUND (discovered) */}
        {tab === "found" && (
          <>
            <div className="screen-title">
              <h1>found</h1>
              <span className="label">
                {q ? `${filteredDiscovered.length}/${discovered.length}` : discovered.length} LISTENING
              </span>
            </div>
            <div className="input-well search">
              <span className="prompt" aria-hidden="true">
                ?
              </span>
              <input
                type="text"
                placeholder="search name, port, framework"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                spellCheck={false}
                aria-label="Search discovered services"
              />
              {query && (
                <button className="icon-btn clear" onClick={() => setQuery("")} aria-label="Clear search" type="button">
                  <X size={12} strokeWidth={3} />
                </button>
              )}
            </div>
            {discovered.length === 0 && <p className="dim">Nothing is listening on this machine right now.</p>}
            {discovered.length > 0 && filteredDiscovered.length === 0 && (
              <p className="dim">No matches for &quot;{query}&quot;.</p>
            )}
            {filteredDiscovered.length > 0 && (
              <ul className="found-list">
                {filteredDiscovered.map((d) => (
                  <li key={d.id} className="found-row">
                    <span className="found-port">:{d.port}</span>
                    <div className="found-info">
                      <span className="found-name">{d.name || `PID ${d.pid}`}</span>
                      <span className="found-detail">{d.page_title || d.framework || d.exe || "-"}</span>
                    </div>
                    {d.is_http ? (
                      <span className="tag http">HTTP {d.http_status}</span>
                    ) : (
                      <span className="tag">TCP</span>
                    )}
                    <button className="btn btn-sm" onClick={() => openAdopt(d)}>
                      Adopt
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}

        {/* TUNNELS */}
        {tab === "tunnels" && (
          <>
            <div className="screen-title">
              <h1>tunnels</h1>
              <span className="label">SSH</span>
            </div>
            {tunnels.length === 0 && <p className="dim">No remote SSH tunnels configured. Add one with prot tunnel.</p>}
            {tunnels.map((t) => (
              <div key={t.config.id} className={`card ${tunnelKind(t.state) === "crash" ? "crashed" : ""}`}>
                <div className="card-head">
                  <span className="card-name">{t.config.alias}</span>
                  <Status kind={tunnelKind(t.state)} text={t.state.toUpperCase()} />
                </div>
                <div className="card-line">
                  <code>
                    {t.config.ssh_host} &rarr; {t.config.remote_host}:{t.config.remote_port}
                  </code>
                  <span className="port">:{t.assigned_local_port || t.config.local_port}</span>
                </div>
                {t.last_error && <span className="dim">{t.last_error}</span>}
                <div className="card-actions">
                  {t.state === "connected" ? (
                    <button className="btn btn-sm btn-danger" onClick={() => run(() => client.stopTunnel(t.config.id))}>
                      Disconnect
                    </button>
                  ) : (
                    <button className="btn btn-sm btn-go" onClick={() => run(() => client.startTunnel(t.config.id))}>
                      Connect
                    </button>
                  )}
                </div>
              </div>
            ))}
          </>
        )}

        {/* SETTINGS */}
        {tab === "settings" && (
          <>
            <div className="screen-title">
              <h1>settings</h1>
            </div>
            {daemonInfo}
            <Field label="Admin API URL" value={adminUrl} onChange={setAdminUrl} prompt="@" />
            <Field label="Auth token" type="password" value={authToken} onChange={setAuthToken} prompt="#" />
            <div>
              <button className="btn btn-primary" onClick={saveSettings}>
                Save
              </button>
            </div>

            <div className="stripe-rule" aria-hidden="true" />

            <div className="card-head">
              <span className="label">MCP CONFIG</span>
              <button
                className="btn btn-sm"
                onClick={() => {
                  navigator.clipboard.writeText(mcpConfigSnippet);
                  setCopiedMCP(true);
                  setTimeout(() => setCopiedMCP(false), 2000);
                }}
              >
                {copiedMCP ? <Check size={14} strokeWidth={2.5} /> : <Copy size={14} strokeWidth={2.5} />}
                {copiedMCP ? "Copied" : "Copy"}
              </button>
            </div>
            <p className="dim">Add to your Claude Desktop, Cursor, or pi MCP config.</p>
            <pre className="code-block">{mcpConfigSnippet}</pre>
          </>
        )}
      </main>

      <nav className="bottom-nav" aria-label="Main">
        <button className={`nav-tab ${tab === "servers" ? "active" : ""}`} onClick={() => setTab("servers")}>
          Servers <span className="nav-count">{services.length}</span>
        </button>
        <button className={`nav-tab ${tab === "found" ? "active" : ""}`} onClick={() => setTab("found")}>
          Found <span className="nav-count">{discovered.length}</span>
        </button>
        <button className={`nav-tab ${tab === "tunnels" ? "active" : ""}`} onClick={() => setTab("tunnels")}>
          Tunnels <span className="nav-count">{tunnels.length}</span>
        </button>
      </nav>

      {toast && (
        <div className={`toast ${toast.ok ? "ok" : ""}`} role="alert">
          <div className="bar" aria-hidden="true" />
          <div className="msg">
            <span className={`warn-icon ${toast.ok ? "ok" : ""}`} aria-hidden="true">
              {toast.ok ? "" : "!"}
            </span>
            <span>{toast.text}</span>
            <button className="icon-btn" onClick={() => setToast(null)} aria-label="Dismiss">
              <X size={14} strokeWidth={2.5} />
            </button>
          </div>
        </div>
      )}

      {/* Logs sheet */}
      {logService && (
        <div className="sheet">
          <div className="sheet-head">
            <h2>logs</h2>
            <Status kind={serviceKind(logService.status)} />
            <button className="icon-btn" onClick={() => setLogService(null)} aria-label="Close">
              <X size={16} strokeWidth={2.5} />
            </button>
          </div>
          <div className="terminal">
            <div className="terminal-bar">
              <span>logs - {logService.name}</span>
              <span>tail -f</span>
            </div>
            <div className="terminal-screen">
              {logs.length === 0 ? (
                <div>&gt; no output yet_</div>
              ) : (
                logs.map((l, i) => (
                  <div key={i} className={`log-line ${l.stream}`}>
                    <span className="log-time">{new Date(l.timestamp).toLocaleTimeString()}</span>
                    <span className="log-msg">{l.message}</span>
                  </div>
                ))
              )}
              <div ref={logEnd} />
              <div className="scanlines" aria-hidden="true" />
            </div>
          </div>
        </div>
      )}

      {/* New server sheet */}
      {showAdd && (
        <form className="sheet" onSubmit={handleCreate}>
          <div className="sheet-head">
            <h2>new server</h2>
            <button type="button" className="icon-btn" onClick={() => setShowAdd(false)} aria-label="Close">
              <X size={16} strokeWidth={2.5} />
            </button>
          </div>
          <div className="sheet-body">
            <Field label="Name" required autoFocus placeholder="Frontend Web" value={svcName} onChange={setSvcName} />
            <Field label="Command" required placeholder="pnpm dev" value={svcCmd} onChange={setSvcCmd} />
            <div className="form-row">
              <Field label="Alias" placeholder="web" prompt="@" value={svcAlias} onChange={setSvcAlias} />
              <Field label="Port" type="number" placeholder="3000" prompt=":" value={svcPort} onChange={setSvcPort} />
            </div>
            <Field label="Working dir" placeholder="/path/to/project" prompt="/" value={svcCwd} onChange={setSvcCwd} />
            <label className="check">
              <input type="checkbox" checked={svcAutoPort} onChange={(e) => setSvcAutoPort(e.target.checked)} />
              Assign a free port automatically
            </label>
          </div>
          <div className="sheet-foot">
            <button type="button" className="btn" onClick={() => setShowAdd(false)}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary">
              Create
            </button>
          </div>
        </form>
      )}

      {/* Edit server sheet */}
      {editForm && (
        <form className="sheet" onSubmit={handleEditSave}>
          <div className="sheet-head">
            <h2>edit server</h2>
            <button type="button" className="icon-btn" onClick={() => setEditForm(null)} aria-label="Close">
              <X size={16} strokeWidth={2.5} />
            </button>
          </div>
          <div className="sheet-body">
            <Field
              label="Name"
              required
              autoFocus
              value={editForm.name}
              onChange={(v) => setEditForm({ ...editForm, name: v })}
            />
            <Field
              label="Alias (.localhost)"
              required
              prompt="@"
              value={editForm.alias}
              onChange={(v) => setEditForm({ ...editForm, alias: v })}
            />
            <Field
              label="Command"
              required
              placeholder="pnpm dev"
              value={editForm.command}
              onChange={(v) => setEditForm({ ...editForm, command: v })}
            />
            <Field
              label="Working dir"
              placeholder="/path/to/project"
              prompt="/"
              value={editForm.cwd}
              onChange={(v) => setEditForm({ ...editForm, cwd: v })}
            />
            <div className="form-row">
              <Field
                label="Port"
                type="number"
                placeholder="3000"
                prompt=":"
                value={editForm.port}
                onChange={(v) => setEditForm({ ...editForm, port: v })}
              />
              <Select
                id="restart-policy"
                label="Restart"
                value={editForm.restartPolicy}
                options={RESTART_POLICIES}
                onChange={(v) => setEditForm({ ...editForm, restartPolicy: v })}
              />
            </div>
            <div className="field">
              <label htmlFor="env-text">Environment</label>
              <div className="input-well">
                <textarea
                  id="env-text"
                  rows={4}
                  placeholder={"KEY=value\nNODE_ENV=development"}
                  value={editForm.env}
                  spellCheck={false}
                  onChange={(e) => setEditForm({ ...editForm, env: e.target.value })}
                />
              </div>
            </div>
            <label className="check">
              <input
                type="checkbox"
                checked={editForm.autoStart}
                onChange={(e) => setEditForm({ ...editForm, autoStart: e.target.checked })}
              />
              Start automatically when protd starts
            </label>
            <label className="check">
              <input
                type="checkbox"
                checked={editForm.autoPort}
                onChange={(e) => setEditForm({ ...editForm, autoPort: e.target.checked })}
              />
              Assign a free port automatically (passed as $PORT)
            </label>
            <label className="check">
              <input
                type="checkbox"
                checked={editForm.lanExposed}
                onChange={(e) => setEditForm({ ...editForm, lanExposed: e.target.checked })}
              />
              Allow access from other devices on the network
            </label>
          </div>
          <div className="sheet-foot">
            <button type="button" className="btn" onClick={() => setEditForm(null)}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary">
              Save
            </button>
          </div>
        </form>
      )}

      {/* Adopt sheet */}
      {adopting && (
        <form className="sheet" onSubmit={handleAdopt}>
          <div className="sheet-head">
            <h2>adopt</h2>
            <button type="button" className="icon-btn" onClick={() => setAdopting(null)} aria-label="Close">
              <X size={16} strokeWidth={2.5} />
            </button>
          </div>
          <div className="sheet-body">
            <div className="card">
              <div className="card-head">
                <span className="card-name">{adopting.name || `PID ${adopting.pid}`}</span>
                <span className="port">:{adopting.port}</span>
              </div>
              <span className="dim">{adopting.cmdline || adopting.exe}</span>
            </div>
            <Field label="Alias (.localhost)" required autoFocus prompt="@" value={adoptAlias} onChange={setAdoptAlias} />
            <p className="dim">Reachable at http://{adoptAlias || "alias"}.localhost</p>
          </div>
          <div className="sheet-foot">
            <button type="button" className="btn" onClick={() => setAdopting(null)}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary">
              Adopt
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
