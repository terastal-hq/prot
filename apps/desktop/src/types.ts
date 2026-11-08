export interface ServiceView {
  id: string;
  name: string;
  runtime: string;
  status: "stopped" | "starting" | "running" | "unhealthy" | "crashed";
  alias: string;
  canonical_url?: string;
  direct_url?: string;
  command?: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  port: number;
  auto_port: boolean;
  auto_start: boolean;
  restart_policy: string;
  lan_exposed: boolean;
  pid?: number;
  created_at: string;
  updated_at: string;
  framework?: string;
}

export interface DiscoveredProcess {
  id: string;
  pid: number;
  name: string;
  exe: string;
  cmdline: string;
  cwd: string;
  username: string;
  create_time: number;
  ip: string;
  port: number;
  family: string;
  is_http: boolean;
  http_status?: number;
  http_server?: string;
  framework?: string;
  page_title?: string;
  discovered_at: string;
}

export interface TunnelStatus {
  config: {
    id: string;
    alias: string;
    ssh_host: string;
    ssh_user?: string;
    remote_host: string;
    remote_port: number;
    local_port: number;
    auto_connect: boolean;
  };
  state: "disconnected" | "connecting" | "connected" | "reconnecting" | "failed";
  assigned_local_port: number;
  pid?: number;
  last_error?: string;
  reconnections: number;
}

export interface DaemonHealth {
  status: string;
  version: string;
  pid: number;
  gateway_addr: string;
  admin_addr: string;
  timestamp: string;
}

export interface LogEntry {
  timestamp: string;
  stream: "stdout" | "stderr" | "system";
  message: string;
}
