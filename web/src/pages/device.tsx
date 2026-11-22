import { useState, type FormEvent } from "react";
import { ChevronRight, Pause, Play, Power, Trash2 } from "lucide-react";
import { toast } from "@/lib/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Select } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Confirm, CopyButton, DeviceStatus, EmptyState, ErrorNote, PageHeader, TunnelStatus } from "@/components/common";
import { api, errorText, useApi } from "@/lib/api";
import { fmtBytes, ago } from "@/lib/format";
import { NAME_RE, PRESETS, type Preset } from "@/lib/presets";
import type { Device, Tunnel } from "@/lib/types";

export function DevicePage({ id }: { id: string }) {
  const device = useApi<Device>(`/devices/${id}`, 10000);
  const tunnels = useApi<Tunnel[]>(`/devices/${id}/tunnels`, 10000);

  if (device.error && !device.data) {
    return <EmptyState title="Computer not found" description={device.error} action={<Button asChild variant="outline"><a href="#/devices">Back to your computers</a></Button>} />;
  }
  if (!device.data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const d = device.data;
  const list = tunnels.data ?? [];
  const shared = list.filter((t) => t.enabled);
  const next = list.length === 0
    ? "Next: add a tunnel below, then share its address with your friends."
    : shared.length === 0
      ? "All tunnels are off. Turn one on to share it."
      : d.disabled
        ? "Tunnels are paused. Resume them under Settings."
        : d.online
          ? "Ready. Share an address below with your friends."
          : "This computer is offline. Start the agent on it to go live.";

  return (
    <>
      <PageHeader
        title={d.name}
        description={[d.hostname, d.os, d.arch, d.agent_version].filter(Boolean).join(" · ") || "Waiting for the agent to connect"}
        actions={<DeviceStatus online={d.online} paused={d.disabled} />}
      />

      <Card className="mb-6 border-primary/30 bg-primary/5">
        <CardContent className="flex items-start gap-3 pt-5 text-sm">
          <ChevronRight className="mt-0.5 size-4 shrink-0 text-primary" />
          <p>{next}</p>
        </CardContent>
      </Card>

      {shared.length > 0 && (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>Share with your friends</CardTitle>
            <CardDescription>Give them one of these addresses. They do not need to install anything.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3 md:grid-cols-2">
            {shared.map((t) => (
              <div key={t.id} className="flex flex-col gap-2 rounded-lg border p-4">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium">{t.name}</span>
                  <div className="flex gap-1">
                    <Badge variant="outline">{t.type.toUpperCase()}</Badge>
                    <TunnelStatus enabled={t.enabled} live={Boolean(t.live?.online)} />
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <code className="rounded bg-muted px-2 py-1 font-mono text-sm break-all">{t.public_address}</code>
                  <CopyButton text={t.public_address} />
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <div className="mb-6 space-y-3">
        <h2 className="text-lg font-semibold">Tunnels</h2>
        {tunnels.error && <ErrorNote message={tunnels.error} />}
        {list.length === 0 ? (
          <EmptyState title="No tunnels yet" description="A tunnel opens one public address for one service on this computer." />
        ) : (
          <Card>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Address</TableHead>
                  <TableHead>Local</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Today</TableHead>
                  <TableHead className="text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {list.map((t) => (
                  <TunnelRow key={t.id} deviceId={id} t={t} onChange={() => { tunnels.reload(); device.reload(); }} />
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </div>

      <AddTunnel deviceId={id} onCreated={tunnels.reload} />

      <SettingsCard device={d} onChanged={() => { device.reload(); }} />
    </>
  );
}

function TunnelRow({ deviceId, t, onChange }: { deviceId: string; t: Tunnel; onChange: () => void }) {
  async function toggle() {
    try {
      await api("PATCH", `/devices/${deviceId}/tunnels/${t.id}`, { enabled: !t.enabled });
      toast.success(t.enabled ? "Tunnel turned off" : "Tunnel turned on");
      onChange();
    } catch (e) {
      toast.error(errorText(e));
    }
  }

  async function remove(): Promise<boolean> {
    try {
      await api("DELETE", `/devices/${deviceId}/tunnels/${t.id}`);
      toast.success("Tunnel deleted");
      onChange();
      return true;
    } catch (e) {
      toast.error(errorText(e));
      return false;
    }
  }

  const today = t.live ? fmtBytes(t.live.today_bytes_in + t.live.today_bytes_out) : "–";
  return (
    <TableRow>
      <TableCell>
        <a href={`#/devices/${deviceId}/tunnels/${t.id}`} className="font-medium hover:underline">{t.name}</a>
        <div className="text-xs text-muted-foreground">
          {t.type.toUpperCase()}{t.proxy_protocol ? ` · PROXY ${t.proxy_protocol}` : ""}
        </div>
      </TableCell>
      <TableCell><code className="font-mono text-xs">{t.public_address}</code></TableCell>
      <TableCell className="text-muted-foreground">{`${t.local_ip}:${t.local_port}`}</TableCell>
      <TableCell><TunnelStatus enabled={t.enabled} live={Boolean(t.live?.online)} /></TableCell>
      <TableCell className="text-right tabular-nums">{today}</TableCell>
      <TableCell>
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={toggle}>
            <Power />
            {t.enabled ? "Turn off" : "Turn on"}
          </Button>
          <Confirm
            title="Delete tunnel?"
            description={`"${t.name}" will stop. Its address (${t.public_address}) is kept for a short time before anyone else can get it.`}
            confirmLabel="Delete"
            destructive
            onConfirm={remove}
            trigger={<Button variant="ghost" size="sm" className="text-destructive"><Trash2 />Delete</Button>}
          />
        </div>
      </TableCell>
    </TableRow>
  );
}

function AddTunnel({ deviceId, onCreated }: { deviceId: string; onCreated: () => void }) {
  const [preset, setPreset] = useState<Preset>(PRESETS[0]);
  const [name, setName] = useState(PRESETS[0].name);
  const [type, setType] = useState<"tcp" | "udp">(PRESETS[0].type);
  const [port, setPort] = useState<string>(String(PRESETS[0].port));
  const [localIp, setLocalIp] = useState("127.0.0.1");
  const [proxy, setProxy] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function pick(p: Preset) {
    setPreset(p);
    setName(p.name);
    setType(p.type);
    setPort(String(p.port));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (!NAME_RE.test(name)) {
      setError("Name: use lower-case letters, numbers and dashes, for example minecraft.");
      return;
    }
    const body: Record<string, unknown> = { name, type, local_port: Number(port), remote_port: 0 };
    if (localIp.trim()) body.local_ip = localIp.trim();
    if (type === "tcp" && proxy) body.proxy_protocol = proxy;
    setBusy(true);
    try {
      await api("POST", `/devices/${deviceId}/tunnels`, body);
      toast.success("Tunnel created");
      onCreated();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle>Add a tunnel</CardTitle>
        <CardDescription>Pick what you want to share. The public port is assigned automatically.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap gap-2" role="group" aria-label="Presets">
          {PRESETS.map((p) => (
            <Button key={p.id} type="button" size="sm" variant={preset.id === p.id ? "default" : "outline"} onClick={() => pick(p)}>
              {p.label}
            </Button>
          ))}
        </div>
        <p className="text-sm text-muted-foreground">{preset.hint}</p>

        <form onSubmit={submit} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <div className="space-y-2">
            <Label htmlFor="tname">Name</Label>
            <Input id="tname" required maxLength={48} value={name} placeholder="minecraft" onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="ttype">Protocol</Label>
            <Select id="ttype" value={type} onChange={(e) => setType(e.target.value as "tcp" | "udp")}>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </Select>
          </div>
          <div className="space-y-2">
            <Label htmlFor="tport">Local port</Label>
            <Input id="tport" type="number" min={1} max={65535} required value={port} onChange={(e) => setPort(e.target.value)} />
            <p className="text-xs text-muted-foreground">The port your game or app uses on this computer.</p>
          </div>
          <details className="sm:col-span-2 lg:col-span-3">
            <summary className="cursor-pointer text-sm text-muted-foreground">Advanced</summary>
            <div className="mt-4 grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="tip">Local address</Label>
                <Input id="tip" value={localIp} maxLength={45} onChange={(e) => setLocalIp(e.target.value)} />
                <p className="text-xs text-muted-foreground">Keep 127.0.0.1 if the service runs on this computer.</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="tproxy">Send player IP (TCP only)</Label>
                <Select id="tproxy" value={proxy} disabled={type !== "tcp"} onChange={(e) => setProxy(e.target.value)}>
                  <option value="">Off</option>
                  <option value="v2">PROXY v2</option>
                  <option value="v1">PROXY v1</option>
                </Select>
              </div>
            </div>
          </details>
          <div className="sm:col-span-2 lg:col-span-3 space-y-3">
            {error && <ErrorNote message={error} />}
            <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create tunnel"}</Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function SettingsCard({ device, onChanged }: { device: Device; onChanged: () => void }) {
  const [name, setName] = useState(device.name);

  async function rename(e: FormEvent) {
    e.preventDefault();
    try {
      await api("PATCH", `/devices/${device.id}`, { name: name.trim() });
      toast.success("Name saved");
      onChanged();
    } catch (err) {
      toast.error(errorText(err));
    }
  }

  async function setPaused(paused: boolean) {
    try {
      await api("PATCH", `/devices/${device.id}`, { disabled: paused });
      toast.success(paused ? "Tunnels paused" : "Tunnels resumed");
      onChanged();
    } catch (err) {
      toast.error(errorText(err));
    }
  }

  async function remove(): Promise<boolean> {
    try {
      await api("DELETE", `/devices/${device.id}`);
      toast.success("Computer removed");
      location.hash = "#/devices";
      return true;
    } catch (err) {
      toast.error(errorText(err));
      return false;
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Settings</CardTitle>
        <CardDescription>Rename this computer, pause all its tunnels, or remove it.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <form onSubmit={rename} className="flex flex-col gap-3 sm:flex-row sm:items-end">
          <div className="flex-1 space-y-2">
            <Label htmlFor="dname">Name</Label>
            <Input id="dname" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <Button type="submit" variant="outline">Save name</Button>
        </form>

        <div className="flex flex-col gap-3 border-t pt-6 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p className="font-medium">{device.disabled ? "Resume all tunnels" : "Pause all tunnels"}</p>
            <p className="text-sm text-muted-foreground">
              {device.disabled ? "Players will be able to connect again." : "Players are disconnected until you resume."}
            </p>
          </div>
          <Button variant="outline" onClick={() => setPaused(!device.disabled)}>
            {device.disabled ? <Play /> : <Pause />}
            {device.disabled ? "Resume" : "Pause"}
          </Button>
        </div>

        <div className="flex flex-col gap-3 border-t pt-6 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <p className="font-medium text-destructive">Remove this computer</p>
            <p className="text-sm text-muted-foreground">Its tunnels are closed and addresses released. The agent cannot reconnect.</p>
          </div>
          <Confirm
            title="Remove this computer?"
            description="Its tunnels are closed and its addresses are released. The agent cannot reconnect afterwards."
            confirmLabel="Remove"
            destructive
            onConfirm={remove}
            trigger={<Button variant="destructive">Remove computer</Button>}
          />
        </div>
        <p className="text-xs text-muted-foreground">Last seen {ago(device.last_seen_at)} from {device.last_ip || "–"}</p>
      </CardContent>
    </Card>
  );
}
