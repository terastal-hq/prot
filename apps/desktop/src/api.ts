import { DaemonHealth, DiscoveredProcess, LogEntry, NewTunnel, ServiceView, SSHMachine, TunnelPatch, TunnelStatus } from "./types";

export class ProtClient {
  private baseUrl: string;
  private token: string;

  constructor() {
    this.baseUrl = localStorage.getItem("prot_admin_url") || "http://127.0.0.1:4400";
    this.token = localStorage.getItem("prot_token") || "";
  }

  async init() {
    const tauri = (window as any).__TAURI__;
    if (tauri?.core?.invoke) {
      try {
        const token = await tauri.core.invoke("get_auth_token");
        if (token && typeof token === "string") {
          this.setToken(token);
        }
      } catch {
        // Fallback to localStorage
      }
      try {
        const url = await tauri.core.invoke("get_admin_url");
        if (url && typeof url === "string") {
          this.setBaseUrl(url);
        }
      } catch {
        // Fallback to localStorage
      }
    }
  }

  setBaseUrl(url: string) {
    this.baseUrl = url;
    localStorage.setItem("prot_admin_url", url);
  }

  setToken(token: string) {
    this.token = token;
    localStorage.setItem("prot_token", token);
  }

  getBaseUrl() {
    return this.baseUrl;
  }

  getToken() {
    return this.token;
  }

  private async request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const headers = new Headers(options.headers || {});
    if (this.token) {
      headers.set("Authorization", `Bearer ${this.token}`);
    }
    if (options.body && typeof options.body === "string") {
      headers.set("Content-Type", "application/json");
    }

    const res = await fetch(`${this.baseUrl}${path}`, {
      ...options,
      headers,
    });

    if (!res.ok) {
      const errBody = await res.text();
      throw new Error(`API error ${res.status}: ${errBody}`);
    }

    if (res.status === 204) {
      return {} as T;
    }

    return res.json() as Promise<T>;
  }

  async getHealth(): Promise<DaemonHealth> {
    return this.request<DaemonHealth>("/v1/health");
  }

  async listServices(): Promise<ServiceView[]> {
    const res = await this.request<{ services: ServiceView[] }>("/v1/services");
    return res.services || [];
  }

  async createService(svc: Partial<ServiceView>): Promise<ServiceView> {
    return this.request<ServiceView>("/v1/services", {
      method: "POST",
      body: JSON.stringify(svc),
    });
  }

  async startService(id: string): Promise<ServiceView> {
    return this.request<ServiceView>(`/v1/services/${id}/start`, { method: "POST" });
  }

  async stopService(id: string): Promise<ServiceView> {
    return this.request<ServiceView>(`/v1/services/${id}/stop`, { method: "POST" });
  }

  async restartService(id: string): Promise<ServiceView> {
    return this.request<ServiceView>(`/v1/services/${id}/restart`, { method: "POST" });
  }

  async updateService(id: string, patch: Partial<ServiceView>): Promise<ServiceView> {
    return this.request<ServiceView>(`/v1/services/${id}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  async renameAlias(id: string, alias: string): Promise<ServiceView> {
    return this.updateService(id, { alias });
  }

  async deleteService(id: string): Promise<void> {
    return this.request<void>(`/v1/services/${id}`, { method: "DELETE" });
  }

  async getLogs(id: string, limit = 100): Promise<LogEntry[]> {
    const res = await this.request<{ logs: LogEntry[] }>(`/v1/services/${id}/logs?limit=${limit}`);
    return res.logs || [];
  }

  async listDiscovery(): Promise<DiscoveredProcess[]> {
    const res = await this.request<{ services: DiscoveredProcess[] }>("/v1/discovery");
    return res.services || [];
  }

  async adoptDiscovery(id: string, data: { name?: string; alias?: string; command?: string }): Promise<ServiceView> {
    return this.request<ServiceView>(`/v1/discovery/${id}/adopt`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** Machine names defined in the user's ~/.ssh/config. */
  async listSSHHosts(): Promise<string[]> {
    const res = await this.request<{ hosts: string[] }>("/v1/ssh/hosts");
    return res.hosts || [];
  }

  async resolveSSHHost(alias: string): Promise<SSHMachine> {
    return this.request<SSHMachine>(`/v1/ssh/hosts/${encodeURIComponent(alias)}`);
  }

  async listTunnels(): Promise<TunnelStatus[]> {
    const res = await this.request<{ tunnels: TunnelStatus[] }>("/v1/tunnels");
    return res.tunnels || [];
  }

  async createTunnel(tun: NewTunnel): Promise<TunnelStatus> {
    return this.request<TunnelStatus>("/v1/tunnels", {
      method: "POST",
      body: JSON.stringify(tun),
    });
  }

  /** Edits a tunnel in place; one that was connected reconnects with the new settings. */
  async updateTunnel(id: string, patch: TunnelPatch): Promise<TunnelStatus> {
    return this.request<TunnelStatus>(`/v1/tunnels/${id}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  async startTunnel(id: string): Promise<TunnelStatus> {
    return this.request<TunnelStatus>(`/v1/tunnels/${id}/start`, { method: "POST" });
  }

  async stopTunnel(id: string): Promise<TunnelStatus> {
    return this.request<TunnelStatus>(`/v1/tunnels/${id}/stop`, { method: "POST" });
  }

  async setTunnelPassword(id: string, password: string): Promise<TunnelStatus> {
    return this.request<TunnelStatus>(`/v1/tunnels/${id}/password`, {
      method: "PUT",
      body: JSON.stringify({ password }),
    });
  }

  async clearTunnelPassword(id: string): Promise<TunnelStatus> {
    return this.request<TunnelStatus>(`/v1/tunnels/${id}/password`, { method: "DELETE" });
  }

  async deleteTunnel(id: string): Promise<void> {
    return this.request<void>(`/v1/tunnels/${id}`, { method: "DELETE" });
  }
}

export const client = new ProtClient();
