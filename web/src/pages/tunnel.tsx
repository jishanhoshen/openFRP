import { ArrowLeft } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CopyButton, EmptyState, PageHeader, Stat, TunnelStatus } from "@/components/common";
import { useApi } from "@/lib/api";
import { fmtBytes } from "@/lib/format";
import type { Tunnel, TrafficDay } from "@/lib/types";

export function TunnelPage({ deviceId, tunnelId }: { deviceId: string; tunnelId: string }) {
  const tunnel = useApi<Tunnel>(`/devices/${deviceId}/tunnels/${tunnelId}`, 10000);
  const traffic = useApi<{ days: TrafficDay[] }>(`/devices/${deviceId}/tunnels/${tunnelId}/traffic?days=30`, 30000);

  const back = (
    <a href={`#/devices/${deviceId}`} className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
      <ArrowLeft className="size-4" /> Back to the computer
    </a>
  );

  if (tunnel.error && !tunnel.data) {
    return <EmptyState title="Tunnel not found" description={tunnel.error} action={back} />;
  }
  if (!tunnel.data) return <p className="text-sm text-muted-foreground">Loading…</p>;

  const t = tunnel.data;
  const days = traffic.data?.days ?? [];
  const totals = days.reduce((a, d) => ({ i: a.i + d.bytes_in, o: a.o + d.bytes_out }), { i: 0, o: 0 });
  const live = Boolean(t.live?.online);

  return (
    <>
      <div className="mb-4">{back}</div>
      <PageHeader
        title={t.name}
        description={`${t.type.toUpperCase()} · local ${t.local_ip}:${t.local_port}`}
        actions={<TunnelStatus enabled={t.enabled} live={live} />}
      />

      <Card className="mb-6 border-primary/40">
        <CardHeader>
          <CardTitle>Share this address</CardTitle>
          <CardDescription>
            {t.type === "udp"
              ? "UDP address. Players enter it in the game's connect screen."
              : "TCP address. Players connect with host:port."}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-center gap-3">
          <code className="rounded-md bg-muted px-3 py-2 font-mono text-lg break-all">{t.public_address}</code>
          <CopyButton text={t.public_address} label="Copy address" />
        </CardContent>
      </Card>

      <div className="mb-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Received, 30 days" value={fmtBytes(totals.i)} />
        <Stat label="Sent, 30 days" value={fmtBytes(totals.o)} />
        <Stat label="Players connected" value={live ? "Yes" : "No"} />
        <Stat label="Open connections" value={t.live?.connections ?? 0} />
      </div>

      <h2 className="mb-3 text-lg font-semibold">Daily traffic</h2>
      {days.length === 0 ? (
        <EmptyState title="No traffic yet" description="Traffic appears here once players connect." />
      ) : (
        <Card>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Day (UTC)</TableHead>
                <TableHead className="text-right">Received</TableHead>
                <TableHead className="text-right">Sent</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {days.slice().reverse().map((d) => (
                <TableRow key={d.day}>
                  <TableCell>{d.day}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtBytes(d.bytes_in)}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtBytes(d.bytes_out)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <p className="mt-4 text-xs text-muted-foreground">
        <Badge variant="outline" className="mr-2">Tip</Badge>
        Statistics refresh every few seconds while the tunnel is live.
      </p>
    </>
  );
}
