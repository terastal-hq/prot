import React, { useState, useEffect } from "react";
import {
  LayoutDashboard,
  Server,
  Network,
  FolderGit2,
  Settings,
  Play,
  Square,
  RotateCw,
  Trash2,
  Terminal,
  ExternalLink,
  Plus,
  RefreshCw,
  Copy,
  Check,
} from "lucide-react";
import { client } from "./api";
import { DaemonHealth, DiscoveredProcess, LogEntry, ServiceView, TunnelStatus } from "./types";
import "./App.css";

export function App() {
  const [activeTab, setActiveTab] = useState<"overview" | "services" | "connections" | "workspaces" | "settings">("overview");
  const [health, setHealth] = useState<DaemonHealth | null>(null);
  const [isOnline, setIsOnline] = useState<boolean>(false);
  const [services, setServices] = useState<ServiceView[]>([]);
  const [discovered, setDiscovered] = useState<DiscoveredProcess[]>([]);
  const [tunnels, setTunnels] = useState<TunnelStatus[]>([]);

  // Logs modal
  const [selectedService, setSelectedService] = useState<ServiceView | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [showLogs, setShowLogs] = useState<boolean>(false);

  // New service modal
  const [showAddModal, setShowAddModal] = useState<boolean>(false);
  const [newSvcName, setNewSvcName] = useState("");
  const [newSvcCmd, setNewSvcCmd] = useState("");
  const [newSvcAlias, setNewSvcAlias] = useState("");
  const [newSvcPort, setNewSvcPort] = useState("");
  const [newSvcCwd, setNewSvcCwd] = useState("");
  const [newSvcAutoPort, setNewSvcAutoPort] = useState(false);

  // Settings
  const [adminUrl, setAdminUrl] = useState(client.getBaseUrl());
  const [authToken, setAuthToken] = useState(client.getToken());
  const [copiedMCP, setCopiedMCP] = useState(false);

  // Fetch daemon state
  const refreshData = async () => {
    try {
      const h = await client.getHealth();
      setHealth(h);
      setIsOnline(true);

      const svcs = await client.listServices();
      setServices(svcs);

      const disc = await client.listDiscovery();
      setDiscovered(disc);

      const tuns = await client.listTunnels();
      setTunnels(tuns);
    } catch (err) {
      setIsOnline(false);
    }
  };

  useEffect(() => {
    const initApp = async () => {
      await client.init();
      setAuthToken(client.getToken());
      setAdminUrl(client.getBaseUrl());
      await refreshData();
    };
    initApp();
    const interval = setInterval(refreshData, 3000);
    return () => clearInterval(interval);
  }, []);

  // Poll logs when log drawer is open
  useEffect(() => {
    if (!showLogs || !selectedService) return;
    const fetchLogs = async () => {
      try {
        const l = await client.getLogs(selectedService.id, 100);
        setLogs(l);
      } catch (err) {
        console.error(err);
      }
    };
    fetchLogs();
    const interval = setInterval(fetchLogs, 1500);
    return () => clearInterval(interval);
  }, [showLogs, selectedService]);

  const handleStart = async (id: string) => {
    try {
      await client.startService(id);
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleStop = async (id: string) => {
    try {
      await client.stopService(id);
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleRestart = async (id: string) => {
    try {
      await client.restartService(id);
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleDelete = async (id: string) => {
    if (!confirm("Are you sure you want to remove this service?")) return;
    try {
      await client.deleteService(id);
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleCreateService = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await client.createService({
        name: newSvcName,
        command: newSvcCmd,
        alias: newSvcAlias || newSvcName.toLowerCase().replace(/\s+/g, "-"),
        port: newSvcPort ? parseInt(newSvcPort, 10) : 0,
        cwd: newSvcCwd,
        auto_port: newSvcAutoPort,
        restart_policy: "on-failure",
      });
      setShowAddModal(false);
      setNewSvcName("");
      setNewSvcCmd("");
      setNewSvcAlias("");
      setNewSvcPort("");
      setNewSvcCwd("");
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const handleAdopt = async (disc: DiscoveredProcess) => {
    const alias = prompt("Enter .localhost alias for this service:", disc.name || `app-${disc.port}`);
    if (!alias) return;
    try {
      await client.adoptDiscovery(disc.id, {
        name: disc.name || `Service on :${disc.port}`,
        alias,
        command: disc.cmdline,
      });
      refreshData();
    } catch (err: any) {
      alert(err.message);
    }
  };

  const saveSettings = () => {
    client.setBaseUrl(adminUrl);
    client.setToken(authToken);
    refreshData();
    alert("Settings saved!");
  };

  const mcpConfigSnippet = JSON.stringify(
    {
      mcpServers: {
        prot: {
          command: "prot",
          args: ["mcp"],
        },
      },
    },
    null,
    2
  );

  return (
    <div className="app-container">
      {/* Sidebar */}
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-logo">p_</div>
          <div className="brand-text">
            <span className="brand-title">prot</span>
            <span className="brand-subtitle">control plane</span>
          </div>
        </div>

        <nav className="nav-menu">
          <button className={`nav-item ${activeTab === "overview" ? "active" : ""}`} onClick={() => setActiveTab("overview")}>
            <LayoutDashboard size={18} />
            <span>Overview</span>
          </button>
          <button className={`nav-item ${activeTab === "services" ? "active" : ""}`} onClick={() => setActiveTab("services")}>
            <Server size={18} />
            <span>Services</span>
            <span className="badge">{services.length}</span>
          </button>
          <button className={`nav-item ${activeTab === "connections" ? "active" : ""}`} onClick={() => setActiveTab("connections")}>
            <Network size={18} />
            <span>Connections</span>
            <span className="badge">{tunnels.length}</span>
          </button>
          <button className={`nav-item ${activeTab === "workspaces" ? "active" : ""}`} onClick={() => setActiveTab("workspaces")}>
            <FolderGit2 size={18} />
            <span>Workspaces</span>
          </button>
          <button className={`nav-item ${activeTab === "settings" ? "active" : ""}`} onClick={() => setActiveTab("settings")}>
            <Settings size={18} />
            <span>Settings</span>
          </button>
        </nav>

        <div className="daemon-status-box">
          <div className="status-indicator">
            <span className={`status-dot ${isOnline ? "online" : "offline"}`}></span>
            <span className="status-label">{isOnline ? "protd Online" : "protd Disconnected"}</span>
          </div>
          {health && isOnline && (
            <div className="daemon-meta">
              <span>PID: {health.pid}</span>
              <span>v{health.version}</span>
            </div>
          )}
        </div>
      </aside>

      {/* Main Content Area */}
      <main className="main-content">
        <header className="top-bar">
          <div className="top-title">
            <h2>{activeTab.toUpperCase()}</h2>
            <p className="subtitle">Local development server supervisor & reverse proxy</p>
          </div>
          <div className="top-actions">
            <button className="btn btn-secondary" onClick={refreshData}>
              <RefreshCw size={15} />
              <span>Refresh</span>
            </button>
            <button className="btn btn-primary" onClick={() => setShowAddModal(true)}>
              <Plus size={16} />
              <span>New Service</span>
            </button>
          </div>
        </header>

        <div className="content-scroll">
          {/* TAB: OVERVIEW */}
          {activeTab === "overview" && (
            <div className="overview-view">
              <div className="stats-grid">
                <div className="stat-card">
                  <div className="stat-value">{services.filter((s) => s.status === "running").length}</div>
                  <div className="stat-label">Running Services</div>
                </div>
                <div className="stat-card">
                  <div className="stat-value">{discovered.length}</div>
                  <div className="stat-label">Discovered Processes</div>
                </div>
                <div className="stat-card">
                  <div className="stat-value">{tunnels.length}</div>
                  <div className="stat-label">SSH Tunnels</div>
                </div>
                <div className="stat-card">
                  <div className="stat-value">.localhost</div>
                  <div className="stat-label">Proxy Namespace</div>
                </div>
              </div>

              {/* Active Services Section */}
              <div className="section-card">
                <div className="section-header">
                  <h3>Managed Services</h3>
                </div>
                {services.length === 0 ? (
                  <div className="empty-placeholder">
                    <p>No managed services registered yet.</p>
                    <button className="btn btn-secondary" onClick={() => setShowAddModal(true)}>
                      Add Service
                    </button>
                  </div>
                ) : (
                  <table className="data-table">
                    <thead>
                      <tr>
                        <th>Status</th>
                        <th>Name</th>
                        <th>Alias & URL</th>
                        <th>Port</th>
                        <th>PID</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {services.map((s) => (
                        <tr key={s.id}>
                          <td>
                            <span className={`status-pill ${s.status}`}>
                              <span className="dot"></span> {s.status}
                            </span>
                          </td>
                          <td>
                            <strong>{s.name}</strong>
                          </td>
                          <td>
                            {s.canonical_url ? (
                              <a href={s.canonical_url} target="_blank" rel="noreferrer" className="link-ext">
                                {s.canonical_url} <ExternalLink size={12} />
                              </a>
                            ) : (
                              <span className="dim">http://{s.alias}.localhost</span>
                            )}
                          </td>
                          <td>{s.port || "-"}</td>
                          <td>{s.pid || "-"}</td>
                          <td>
                            <div className="action-buttons">
                              {s.status === "running" ? (
                                <button className="icon-btn danger" title="Stop" onClick={() => handleStop(s.id)}>
                                  <Square size={14} />
                                </button>
                              ) : (
                                <button className="icon-btn success" title="Start" onClick={() => handleStart(s.id)}>
                                  <Play size={14} />
                                </button>
                              )}
                              <button className="icon-btn" title="Restart" onClick={() => handleRestart(s.id)}>
                                <RotateCw size={14} />
                              </button>
                              <button
                                className="icon-btn"
                                title="Logs"
                                onClick={() => {
                                  setSelectedService(s);
                                  setShowLogs(true);
                                }}
                              >
                                <Terminal size={14} />
                              </button>
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>

              {/* Discovered Section */}
              <div className="section-card" style={{ marginTop: 24 }}>
                <div className="section-header">
                  <h3>Discovered Local Services ({discovered.length})</h3>
                  <span className="dim-text">Observed TCP listening processes</span>
                </div>
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>Port</th>
                      <th>Process</th>
                      <th>Protocol</th>
                      <th>Details</th>
                      <th>Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {discovered.map((d) => (
                      <tr key={d.id}>
                        <td>
                          <code>:{d.port}</code>
                        </td>
                        <td>
                          <strong>{d.name || `PID ${d.pid}`}</strong>
                        </td>
                        <td>{d.is_http ? <span className="badge-tag http">HTTP {d.http_status}</span> : <span className="badge-tag">TCP</span>}</td>
                        <td>
                          <span className="dim">{d.page_title || d.framework || d.exe}</span>
                        </td>
                        <td>
                          <button className="btn btn-sm btn-secondary" onClick={() => handleAdopt(d)}>
                            Adopt
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {/* TAB: SERVICES */}
          {activeTab === "services" && (
            <div className="services-view">
              <div className="services-grid">
                {services.map((s) => (
                  <div key={s.id} className="service-card">
                    <div className="card-top">
                      <span className={`status-pill ${s.status}`}>
                        <span className="dot"></span> {s.status}
                      </span>
                      <div className="card-actions">
                        <button
                          className="icon-btn"
                          onClick={() => {
                            setSelectedService(s);
                            setShowLogs(true);
                          }}
                        >
                          <Terminal size={14} />
                        </button>
                        <button className="icon-btn danger" onClick={() => handleDelete(s.id)}>
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </div>
                    <h4>{s.name}</h4>
                    <p className="card-url">
                      {s.canonical_url ? (
                        <a href={s.canonical_url} target="_blank" rel="noreferrer">
                          {s.canonical_url} <ExternalLink size={12} />
                        </a>
                      ) : (
                        <span>http://{s.alias}.localhost</span>
                      )}
                    </p>
                    <div className="card-meta">
                      <div>
                        <span className="dim">Command:</span> <code>{s.command || "external"}</code>
                      </div>
                      <div>
                        <span className="dim">Port:</span> {s.port || "auto"}
                      </div>
                      <div>
                        <span className="dim">PID:</span> {s.pid || "-"}
                      </div>
                    </div>
                    <div className="card-bottom">
                      {s.status === "running" ? (
                        <button className="btn btn-secondary danger" onClick={() => handleStop(s.id)}>
                          <Square size={14} /> Stop
                        </button>
                      ) : (
                        <button className="btn btn-primary" onClick={() => handleStart(s.id)}>
                          <Play size={14} /> Start
                        </button>
                      )}
                      <button className="btn btn-secondary" onClick={() => handleRestart(s.id)}>
                        <RotateCw size={14} /> Restart
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* TAB: CONNECTIONS */}
          {activeTab === "connections" && (
            <div className="connections-view">
              <div className="section-card">
                <div className="section-header">
                  <h3>Remote SSH Tunnels</h3>
                </div>
                {tunnels.length === 0 ? (
                  <div className="empty-placeholder">
                    <p>No remote SSH tunnels configured.</p>
                  </div>
                ) : (
                  <table className="data-table">
                    <thead>
                      <tr>
                        <th>Alias</th>
                        <th>Remote Target</th>
                        <th>Local Forward Port</th>
                        <th>State</th>
                        <th>Actions</th>
                      </tr>
                    </thead>
                    <tbody>
                      {tunnels.map((t) => (
                        <tr key={t.config.id}>
                          <td>
                            <strong>{t.config.alias}</strong>
                          </td>
                          <td>
                            {t.config.ssh_host} ({t.config.remote_host}:{t.config.remote_port})
                          </td>
                          <td>
                            <code>:{t.assigned_local_port || t.config.local_port}</code>
                          </td>
                          <td>
                            <span className={`status-pill ${t.state}`}>{t.state}</span>
                          </td>
                          <td>
                            {t.state === "connected" ? (
                              <button className="btn btn-sm btn-secondary danger" onClick={() => client.stopTunnel(t.config.id).then(refreshData)}>
                                Disconnect
                              </button>
                            ) : (
                              <button className="btn btn-sm btn-primary" onClick={() => client.startTunnel(t.config.id).then(refreshData)}>
                                Connect
                              </button>
                            )}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            </div>
          )}

          {/* TAB: WORKSPACES */}
          {activeTab === "workspaces" && (
            <div className="workspaces-view">
              <div className="section-card">
                <div className="section-header">
                  <h3>Project Workspaces</h3>
                </div>
                <div className="empty-placeholder">
                  <p>Workspaces allow grouping multiple related services and SSH tunnels for one-click startup.</p>
                </div>
              </div>
            </div>
          )}

          {/* TAB: SETTINGS */}
          {activeTab === "settings" && (
            <div className="settings-view">
              <div className="section-card">
                <div className="section-header">
                  <h3>protd Daemon Configuration</h3>
                </div>
                <div className="form-group">
                  <label>Admin Management API URL</label>
                  <input type="text" value={adminUrl} onChange={(e) => setAdminUrl(e.target.value)} className="input" />
                </div>
                <div className="form-group">
                  <label>Authentication Token (~/.prot/auth.token)</label>
                  <input type="password" value={authToken} onChange={(e) => setAuthToken(e.target.value)} className="input" />
                </div>
                <button className="btn btn-primary" onClick={saveSettings}>
                  Save Settings
                </button>
              </div>

              <div className="section-card" style={{ marginTop: 24 }}>
                <div className="section-header">
                  <h3>Model Context Protocol (MCP) Setup</h3>
                  <button
                    className="btn btn-sm btn-secondary"
                    onClick={() => {
                      navigator.clipboard.writeText(mcpConfigSnippet);
                      setCopiedMCP(true);
                      setTimeout(() => setCopiedMCP(false), 2000);
                    }}
                  >
                    {copiedMCP ? <Check size={14} /> : <Copy size={14} />} {copiedMCP ? "Copied" : "Copy Config"}
                  </button>
                </div>
                <p className="dim" style={{ marginBottom: 12 }}>
                  Add this to your Claude Desktop, Cursor, or pi MCP client config:
                </p>
                <pre className="code-block">{mcpConfigSnippet}</pre>
              </div>
            </div>
          )}
        </div>
      </main>

      {/* Logs Drawer Modal */}
      {showLogs && selectedService && (
        <div className="modal-overlay" onClick={() => setShowLogs(false)}>
          <div className="logs-modal" onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <div className="modal-title">
                <Terminal size={18} />
                <span>Logs: {selectedService.name}</span>
                <span className={`status-pill ${selectedService.status}`}>{selectedService.status}</span>
              </div>
              <button className="icon-btn" onClick={() => setShowLogs(false)}>
                ✕
              </button>
            </div>
            <div className="logs-console">
              {logs.length === 0 ? (
                <div className="dim">No log output yet.</div>
              ) : (
                logs.map((l, i) => (
                  <div key={i} className={`log-line ${l.stream}`}>
                    <span className="log-time">{new Date(l.timestamp).toLocaleTimeString()}</span>
                    <span className="log-stream">[{l.stream}]</span>
                    <span className="log-msg">{l.message}</span>
                  </div>
                ))
              )}
            </div>
          </div>
        </div>
      )}

      {/* Add Service Modal */}
      {showAddModal && (
        <div className="modal-overlay" onClick={() => setShowAddModal(false)}>
          <div className="modal-card" onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <h3>Register New Service</h3>
              <button className="icon-btn" onClick={() => setShowAddModal(false)}>
                ✕
              </button>
            </div>
            <form onSubmit={handleCreateService}>
              <div className="form-group">
                <label>Service Name *</label>
                <input required placeholder="e.g. Frontend Web" value={newSvcName} onChange={(e) => setNewSvcName(e.target.value)} className="input" />
              </div>
              <div className="form-group">
                <label>Command *</label>
                <input required placeholder="e.g. npm run dev or python main.py" value={newSvcCmd} onChange={(e) => setNewSvcCmd(e.target.value)} className="input" />
              </div>
              <div className="form-row">
                <div className="form-group">
                  <label>Local Alias (.localhost)</label>
                  <input placeholder="e.g. frontend" value={newSvcAlias} onChange={(e) => setNewSvcAlias(e.target.value)} className="input" />
                </div>
                <div className="form-group">
                  <label>Port</label>
                  <input placeholder="e.g. 3000" type="number" value={newSvcPort} onChange={(e) => setNewSvcPort(e.target.value)} className="input" />
                </div>
              </div>
              <div className="form-group">
                <label>Working Directory</label>
                <input placeholder="/path/to/project" value={newSvcCwd} onChange={(e) => setNewSvcCwd(e.target.value)} className="input" />
              </div>
              <div className="form-group" style={{ display: "flex", alignItems: "center", gap: 8 }}>
                <input id="autoPort" type="checkbox" checked={newSvcAutoPort} onChange={(e) => setNewSvcAutoPort(e.target.checked)} />
                <label htmlFor="autoPort" style={{ margin: 0, cursor: "pointer" }}>Automatically assign free port</label>
              </div>
              <div className="form-actions">
                <button type="button" className="btn btn-secondary" onClick={() => setShowAddModal(false)}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary">
                  Create Service
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  );
}
