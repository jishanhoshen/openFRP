export interface Me {
  id: number;
  email: string;
  role: "user" | "admin";
  disabled: boolean;
  created_at: string;
}

export interface Info {
  version: string;
  public_host: string;
  port_range: [number, number];
  open_registration: boolean;
  max_devices: number;
  max_tunnels: number;
  live_statistics: boolean;
}

export interface Device {
  id: string;
  user_id: number;
  name: string;
  disabled: boolean;
  revoked: boolean;
  paired: boolean;
  created_at: string;
  last_seen_at: string | null;
  last_ip: string;
  hostname: string;
  os: string;
  arch: string;
  agent_version: string;
  online: boolean;
}

export interface Live {
  online: boolean;
  connections: number;
  today_bytes_in: number;
  today_bytes_out: number;
  updated_at: string;
}

export interface Tunnel {
  id: number;
  device_id: string;
  name: string;
  type: "tcp" | "udp";
  remote_port: number;
  local_ip: string;
  local_port: number;
  enabled: boolean;
  bandwidth_limit: string;
  proxy_protocol: string;
  created_at: string;
  public_address: string;
  device_name?: string;
  live?: Live;
}

export interface TrafficDay {
  day: string;
  bytes_in: number;
  bytes_out: number;
}

export interface AuditEntry {
  id: number;
  at: string;
  actor_user_id: number | null;
  actor_device_id?: string;
  action: string;
  target: string;
  ip: string;
}

export interface ApiToken {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
}

export interface ReservedPort {
  type: "tcp" | "udp";
  remote_port: number;
  user_id: number | null;
  user_email?: string;
  note: string;
  in_use_by?: string;
}

export interface AdminUser {
  id: number;
  email: string;
  role: "user" | "admin";
  disabled: boolean;
  max_devices: number | null;
  max_tunnels_per_device: number | null;
}

export interface Overview {
  users: number;
  devices: number;
  devices_online: number;
  tunnels: number;
  ports_in_pool: number;
  today_bytes_in: number;
  today_bytes_out: number;
  last30_bytes_in: number;
  last30_bytes_out: number;
  top_tunnels_30d: { tunnel_id: number; device_id: string; name: string; owner: string; bytes_in: number; bytes_out: number }[];
}

export interface Summary {
  computers: number;
  computers_online: number;
  tunnels: number;
  tunnels_live: number;
  traffic_30_days: TrafficDay[];
  traffic_30_in: number;
  traffic_30_out: number;
}
