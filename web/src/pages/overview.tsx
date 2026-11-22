import { useMemo, useState } from "react";
import { Monitor, Network, Activity, ArrowDownToLine, ArrowUpFromLine } from "lucide-react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Select } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AreaChart, type AreaPoint } from "@/components/area-chart";
import { EmptyState, ErrorNote, PageHeader, TunnelStatus } from "@/components/common";
import { useApi } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { fmtBytes } from "@/lib/format";
import type { Summary, Tunnel, TrafficDay } from "@/lib/types";

/** Fills missing days with zeros so the chart has one point per day. */
function series(days: TrafficDay[], range: number): AreaPoint[] {
  const byDay = new Map(days.map((d) => [d.day, d]));
  const out: AreaPoint[] = [];
  const today = new Date();
  for (let i = range - 1; i >= 0; i--) {
    const d = new Date(Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate() - i));
    const key = d.toISOString().slice(0, 10);
    const hit = byDay.get(key);
    out.push({
      label: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }),
      a: hit?.bytes_in ?? 0,
      b: hit?.bytes_out ?? 0,
    });
  }
  return out;
}

export function OverviewPage() {
  const { me } = useAuth();
  const summary = useApi<Summary>("/me/summary", 30000);
  const tunnels = useApi<Tunnel[]>("/tunnels", 30000);
  const [range, setRange] = useState(30);
  const points = useMemo(() => series(summary.data?.traffic_30_days ?? [], range), [summary.data, range]);
  const rangeTotal = points.reduce((t, p) => ({ a: t.a + p.a, b: t.b + p.b }), { a: 0, b: 0 });

  if (summary.error && !summary.data) return <ErrorNote message={summary.error} />;

  const s = summary.data;
  return (
    <>
      <PageHeader title={`Hello${me ? ", " + me.email.split("@")[0] : ""}`} description="Your computers, tunnels and traffic at a glance." />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard icon={Monitor} title="Computers" value={s ? `${s.computers_online} / ${s.computers}` : "–"}
          note={s && s.computers_online > 0 ? "Online now" : "None online right now"} />
        <StatCard icon={Network} title="Tunnels" value={s ? `${s.tunnels_live} / ${s.tunnels}` : "–"}
          note={s && s.tunnels_live > 0 ? "Live for players" : "Waiting for players or agents"} />
        <StatCard icon={ArrowDownToLine} title="Received, 30 days" value={s ? fmtBytes(s.traffic_30_in) : "–"}
          note="Data from players to your tunnels" />
        <StatCard icon={ArrowUpFromLine} title="Sent, 30 days" value={s ? fmtBytes(s.traffic_30_out) : "–"}
          note="Data from your tunnels to players" />
      </div>

      <Card className="mt-6">
        <CardHeader className="flex-row flex-wrap items-start justify-between gap-3 space-y-0">
          <div className="space-y-1">
            <CardTitle>Traffic</CardTitle>
            <CardDescription>
              {range === 7 ? "Last 7 days" : "Last 30 days"} · {fmtBytes(rangeTotal.a + rangeTotal.b)} in total
            </CardDescription>
          </div>
          <Select className="w-40" aria-label="Range" value={range} onChange={(e) => setRange(Number(e.target.value))}>
            <option value={30}>Last 30 days</option>
            <option value={7}>Last 7 days</option>
          </Select>
        </CardHeader>
        <CardContent>
          {s && s.traffic_30_days.length === 0 ? (
            <div className="flex h-40 items-center justify-center text-sm text-muted-foreground">
              <Activity className="mr-2 size-4" /> No traffic yet. It appears here once players connect.
            </div>
          ) : (
            <AreaChart data={points} aName="Received" bName="Sent" format={fmtBytes} />
          )}
        </CardContent>
      </Card>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Recent tunnels</CardTitle>
          <CardDescription>The latest addresses you have created.</CardDescription>
        </CardHeader>
        <CardContent>
          {tunnels.data && tunnels.data.length === 0 ? (
            <EmptyState title="No tunnels yet" description="Open a computer and add a tunnel to get a public address." />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Tunnel</TableHead>
                  <TableHead>Computer</TableHead>
                  <TableHead>Address</TableHead>
                  <TableHead>Status</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(tunnels.data ?? []).slice(0, 6).map((t) => (
                  <TableRow key={t.id}>
                    <TableCell>
                      <a className="font-medium hover:underline" href={`#/devices/${t.device_id}/tunnels/${t.id}`}>{t.name}</a>
                      <div className="text-xs text-muted-foreground">{t.type.toUpperCase()}</div>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{t.device_name ?? "–"}</TableCell>
                    <TableCell><code className="font-mono text-xs">{t.public_address}</code></TableCell>
                    <TableCell><TunnelStatus enabled={t.enabled} live={Boolean(t.live?.online)} /></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      <p className="mt-4 text-xs text-muted-foreground"><Badge variant="outline" className="mr-2">Note</Badge>Figures refresh automatically every 30 seconds.</p>
    </>
  );
}

function StatCard({ icon: Icon, title, value, note }: { icon: typeof Monitor; title: string; value: string; note: string }) {
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium text-muted-foreground">{title}</CardTitle>
        <Icon className="size-4 text-muted-foreground" />
      </CardHeader>
      <CardContent className="space-y-1">
        <div className="text-2xl font-bold tabular-nums">{value}</div>
        <p className="text-xs text-muted-foreground">{note}</p>
      </CardContent>
    </Card>
  );
}
