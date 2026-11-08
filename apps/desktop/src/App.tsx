import React, { useState, useEffect, useRef, useCallback } from "react";
import { Play, Square, RotateCw, Trash2, Terminal, ExternalLink, Plus, Copy, Check, X } from "lucide-react";
import { client } from "./api";
import { DaemonHealth, DiscoveredProcess, LogEntry, ServiceView, TunnelStatus } from "./types";
import wordmark from "./assets/wordmark.png";
import "./App.css";

type Tab = "servers" | "found" | "tunnels" | "settings";
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

export function App() {
  const [tab, setTab] = useState<Tab>("servers");
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
        <span className={`daemon-chip ${isOnline ? "" : "offline"}`}>
          <span className="sq" aria-hidden="true" />
          {isOnline ? "DAEMON ACTIVE" : "DAEMON OFFLINE"}
        </span>
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
                <span className="summary-chip run">{running} RUNNING</span>
                {crashed > 0 && <span className="summary-chip crash">{crashed} CRASHED</span>}
                <span className="summary-chip">{idle} IDLE</span>
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
                    {s.canonical_url ? (
                      <a className="card-url" href={s.canonical_url} target="_blank" rel="noreferrer">
                        {s.canonical_url} <ExternalLink size={12} strokeWidth={2.5} />
                      </a>
                    ) : (
                      <span className="dim">http://{s.alias}.localhost</span>
                    )}
                    <div className="card-actions">
                      {s.status === "running" ? (
                        <button className="icon-btn danger" title="Stop" onClick={() => run(() => client.stopService(s.id))}>
                          <Square size={14} strokeWidth={2.5} />
                        </button>
                      ) : (
                        <button className="icon-btn go" title="Start" onClick={() => run(() => client.startService(s.id))}>
                          <Play size={14} strokeWidth={2.5} />
                        </button>
                      )}
                      <button className="icon-btn" title="Restart" onClick={() => run(() => client.restartService(s.id))}>
                        <RotateCw size={14} strokeWidth={2.5} />
                      </button>
                      <button className="icon-btn" title="Logs" onClick={() => setLogService(s)}>
                        <Terminal size={14} strokeWidth={2.5} />
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
              <span className="label">{discovered.length} LISTENING</span>
            </div>
            {discovered.length === 0 && <p className="dim">Nothing is listening on this machine right now.</p>}
            {discovered.map((d) => (
              <div key={d.id} className="card">
                <div className="card-head">
                  <span className="card-name">{d.name || `PID ${d.pid}`}</span>
                  {d.is_http ? <span className="tag http">HTTP {d.http_status}</span> : <span className="tag">TCP</span>}
                </div>
                <div className="card-line">
                  <span className="port">:{d.port}</span>
                  <span className="dim">{d.page_title || d.framework || d.exe}</span>
                </div>
                <div className="card-actions">
                  <button className="btn btn-sm" onClick={() => openAdopt(d)}>
                    Adopt
                  </button>
                </div>
              </div>
            ))}
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
        <button className={`nav-tab ${tab === "settings" ? "active" : ""}`} onClick={() => setTab("settings")}>
          Setup
        </button>
      </nav>

      {toast && (
        <div className={`toast ${toast.ok ? "ok" : ""}`} role="alert">
          <div className="bar" aria-hidden="true" />
          <div className="msg">
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
