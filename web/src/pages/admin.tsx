import { useState, type FormEvent } from "react";
import { toast } from "@/lib/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Select } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Confirm, EmptyState, ErrorNote, PageHeader, Stat } from "@/components/common";
import { api, errorText, useApi } from "@/lib/api";
import { ago, fmtBytes } from "@/lib/format";
import { go } from "@/lib/router";
import { useAuth } from "@/lib/auth";
import type { AdminUser, AuditEntry, Overview, ReservedPort } from "@/lib/types";

const TABS = ["overview", "users", "ports", "audit"] as const;
type Tab = (typeof TABS)[number];

export function AdminPage({ tab }: { tab: string }) {
  const { me } = useAuth();
  if (me?.role !== "admin") {
    return <EmptyState title="Administrators only" description="Your account does not have admin access." />;
  }
  const current: Tab = (TABS as readonly string[]).includes(tab) ? (tab as Tab) : "overview";
  return (
    <>
      <PageHeader title="Administration" description="Users, fixed ports, usage and the audit log." />
      <Tabs value={current} onValueChange={(v) => go(`#/admin/${v === "overview" ? "" : v}`)}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="users">Users</TabsTrigger>
          <TabsTrigger value="ports">Ports</TabsTrigger>
          <TabsTrigger value="audit">Audit</TabsTrigger>
        </TabsList>
        <TabsContent value="overview"><OverviewTab /></TabsContent>
        <TabsContent value="users"><UsersTab /></TabsContent>
        <TabsContent value="ports"><PortsTab /></TabsContent>
        <TabsContent value="audit"><AuditTab /></TabsContent>
      </Tabs>
    </>
  );
}

function OverviewTab() {
  const o = useApi<Overview>("/admin/overview", 30000);
  if (o.error && !o.data) return <ErrorNote message={o.error} />;
  if (!o.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  const d = o.data;
  return (
    <div className="space-y-6">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <Stat label="Users" value={d.users} />
        <Stat label="Computers online" value={`${d.devices_online} / ${d.devices}`} />
        <Stat label="Tunnels" value={d.tunnels} />
        <Stat label="Ports in use" value={`${d.tunnels} / ${d.ports_in_pool}`} hint="Automatic pool" />
        <Stat label="Traffic today" value={fmtBytes(d.today_bytes_in + d.today_bytes_out)} />
        <Stat label="Traffic, 30 days" value={fmtBytes(d.last30_bytes_in + d.last30_bytes_out)} />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Busiest tunnels (30 days)</CardTitle>
          <CardDescription>The most used tunnels across all users.</CardDescription>
        </CardHeader>
        <CardContent>
          {d.top_tunnels_30d.length === 0 ? (
            <p className="text-sm text-muted-foreground">No traffic yet.</p>
          ) : (
            <Table>
              <TableHeader><TableRow><TableHead>Tunnel</TableHead><TableHead>Owner</TableHead><TableHead className="text-right">Total</TableHead></TableRow></TableHeader>
              <TableBody>
                {d.top_tunnels_30d.map((t) => (
                  <TableRow key={t.tunnel_id}>
                    <TableCell className="font-medium">{t.name}</TableCell>
                    <TableCell className="text-muted-foreground">{t.owner}</TableCell>
                    <TableCell className="text-right tabular-nums">{fmtBytes(t.bytes_in + t.bytes_out)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function UsersTab() {
  const users = useApi<AdminUser[]>("/admin/users", 30000);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [error, setError] = useState<string | null>(null);

  async function create(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await api("POST", "/admin/users", { email, password, role });
      toast.success("User created");
      setEmail("");
      setPassword("");
      users.reload();
    } catch (err) {
      setError(errorText(err));
    }
  }

  async function patch(id: number, body: Record<string, unknown>, ok: string) {
    try {
      await api("PATCH", `/admin/users/${id}`, body);
      toast.success(ok);
      users.reload();
    } catch (err) {
      toast.error(errorText(err));
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Create a user</CardTitle>
          <CardDescription>Give them the starting password and ask them to change it.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={create} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <div className="space-y-2"><Label htmlFor="ue">Email</Label><Input id="ue" type="email" required value={email} onChange={(e) => setEmail(e.target.value)} /></div>
            <div className="space-y-2"><Label htmlFor="up">Starting password</Label><Input id="up" type="password" minLength={10} required value={password} onChange={(e) => setPassword(e.target.value)} /></div>
            <div className="space-y-2"><Label htmlFor="ur">Role</Label>
              <Select id="ur" value={role} onChange={(e) => setRole(e.target.value as "user" | "admin")}>
                <option value="user">User</option><option value="admin">Admin</option>
              </Select></div>
            <div className="flex items-end"><Button type="submit" className="w-full">Create user</Button></div>
            {error && <div className="sm:col-span-2 lg:col-span-4"><ErrorNote message={error} /></div>}
          </form>
        </CardContent>
      </Card>

      {users.error && <ErrorNote message={users.error} />}
      {users.data && (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Email</TableHead><TableHead>Role</TableHead><TableHead>Computers limit</TableHead>
                <TableHead>Tunnels limit</TableHead><TableHead>Status</TableHead><TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.data.map((u) => (
                <TableRow key={u.id}>
                  <TableCell className="font-medium">{u.email}</TableCell>
                  <TableCell><Badge variant={u.role === "admin" ? "default" : "outline"}>{u.role}</Badge></TableCell>
                  <TableCell><QuotaField user={u} field="max_devices" onSaved={users.reload} /></TableCell>
                  <TableCell><QuotaField user={u} field="max_tunnels_per_device" onSaved={users.reload} /></TableCell>
                  <TableCell>{u.disabled ? <Badge variant="destructive">Disabled</Badge> : <Badge variant="success">Active</Badge>}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-2">
                      <Button size="sm" variant="outline" onClick={() => patch(u.id, { disabled: !u.disabled }, u.disabled ? "User enabled" : "User disabled")}>
                        {u.disabled ? "Enable" : "Disable"}
                      </Button>
                      <Button size="sm" variant="outline" onClick={() => patch(u.id, { role: u.role === "admin" ? "user" : "admin" }, "Role updated")}>
                        {u.role === "admin" ? "Remove admin" : "Make admin"}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function QuotaField({ user, field, onSaved }: { user: AdminUser; field: "max_devices" | "max_tunnels_per_device"; onSaved: () => void }) {
  const current = user[field];
  const [value, setValue] = useState(current === null ? "" : String(current));
  async function save(e: FormEvent) {
    e.preventDefault();
    try {
      await api("PATCH", `/admin/users/${user.id}`, { [field]: value === "" ? -1 : Number(value) });
      toast.success("Limit saved");
      onSaved();
    } catch (err) {
      toast.error(errorText(err));
    }
  }
  return (
    <form onSubmit={save} className="flex items-center gap-2">
      <Input className="h-8 w-20" type="number" min={-1} max={1000} placeholder="default" value={value}
        aria-label={field.replace(/_/g, " ")} onChange={(e) => setValue(e.target.value)} />
      <Button size="sm" variant="ghost" type="submit">Save</Button>
    </form>
  );
}

function PortsTab() {
  const rs = useApi<ReservedPort[]>("/admin/reserved-ports", 30000);
  const [type, setType] = useState<"tcp" | "udp">("tcp");
  const [port, setPort] = useState("");
  const [userId, setUserId] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function reserve(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await api("PUT", `/admin/reserved-ports/${type}/${port}`, { note, user_id: userId === "" ? null : Number(userId) });
      toast.success("Port reserved");
      setPort("");
      setNote("");
      rs.reload();
    } catch (err) {
      setError(errorText(err));
    }
  }

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>Reserve a port</CardTitle>
          <CardDescription>Reserved ports are never given out automatically. Use them for fixed server ports such as 25565 for Minecraft.</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={reserve} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-5">
            <div className="space-y-2"><Label htmlFor="pt">Protocol</Label>
              <Select id="pt" value={type} onChange={(e) => setType(e.target.value as "tcp" | "udp")}><option value="tcp">TCP</option><option value="udp">UDP</option></Select></div>
            <div className="space-y-2"><Label htmlFor="pp">Port</Label><Input id="pp" type="number" min={1} max={65535} required value={port} onChange={(e) => setPort(e.target.value)} /></div>
            <div className="space-y-2"><Label htmlFor="pu">User ID</Label><Input id="pu" type="number" min={1} placeholder="blank = admins only" value={userId} onChange={(e) => setUserId(e.target.value)} /></div>
            <div className="space-y-2"><Label htmlFor="pn">Note</Label><Input id="pn" maxLength={200} value={note} onChange={(e) => setNote(e.target.value)} /></div>
            <div className="flex items-end"><Button type="submit" className="w-full">Reserve</Button></div>
            {error && <div className="sm:col-span-2 lg:col-span-5"><ErrorNote message={error} /></div>}
          </form>
        </CardContent>
      </Card>
      {rs.error && <ErrorNote message={rs.error} />}
      {rs.data && rs.data.length === 0 && <p className="text-sm text-muted-foreground">No reserved ports yet.</p>}
      {rs.data && rs.data.length > 0 && (
        <Card>
          <Table>
            <TableHeader><TableRow><TableHead>Port</TableHead><TableHead>For</TableHead><TableHead>In use by</TableHead><TableHead>Note</TableHead><TableHead className="text-right">Actions</TableHead></TableRow></TableHeader>
            <TableBody>
              {rs.data.map((r) => (
                <TableRow key={`${r.type}-${r.remote_port}`}>
                  <TableCell><code className="font-mono">{r.remote_port}/{r.type}</code></TableCell>
                  <TableCell>{r.user_email || "admins only"}</TableCell>
                  <TableCell className="text-muted-foreground">{r.in_use_by || "–"}</TableCell>
                  <TableCell className="text-muted-foreground">{r.note}</TableCell>
                  <TableCell className="text-right">
                    <Confirm
                      title="Remove reservation?"
                      description={`Port ${r.remote_port}/${r.type} becomes available again.`}
                      confirmLabel="Remove"
                      destructive
                      trigger={<Button size="sm" variant="ghost" className="text-destructive">Remove</Button>}
                      onConfirm={async () => {
                        try {
                          await api("DELETE", `/admin/reserved-ports/${r.type}/${r.remote_port}`);
                          toast.success("Reservation removed");
                          rs.reload();
                          return true;
                        } catch (err) {
                          toast.error(errorText(err));
                          return false;
                        }
                      }}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}

function AuditTab() {
  const audit = useApi<AuditEntry[]>("/admin/audit", 30000);
  if (audit.error && !audit.data) return <ErrorNote message={audit.error} />;
  if (!audit.data) return <p className="text-sm text-muted-foreground">Loading…</p>;
  return (
    <Card>
      <Table>
        <TableHeader><TableRow><TableHead>When</TableHead><TableHead>Who</TableHead><TableHead>Action</TableHead><TableHead>Target</TableHead><TableHead>IP</TableHead></TableRow></TableHeader>
        <TableBody>
          {audit.data.map((e) => (
            <TableRow key={e.id}>
              <TableCell className="text-muted-foreground" title={e.at}>{ago(e.at)}</TableCell>
              <TableCell>{e.actor_user_id ? `user ${e.actor_user_id}` : e.actor_device_id || "system"}</TableCell>
              <TableCell>{e.action}</TableCell>
              <TableCell className="text-muted-foreground">{e.target}</TableCell>
              <TableCell className="font-mono text-xs text-muted-foreground">{e.ip}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
