import { useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { toast } from "@/lib/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Confirm, CopyButton, ErrorNote, PageHeader } from "@/components/common";
import { api, errorText, useApi } from "@/lib/api";
import { ago } from "@/lib/format";
import { useAuth } from "@/lib/auth";
import type { ApiToken, AuditEntry, ReservedPort } from "@/lib/types";

export function AccountPage() {
  const { me } = useAuth();
  return (
    <>
      <PageHeader title="Account" description={`Signed in as ${me?.email ?? ""}`} />
      <div className="grid gap-6 lg:grid-cols-2">
        <PasswordCard />
        <TokensCard />
        <ReservedCard />
        <ActivityCard />
      </div>
    </>
  );
}

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api("POST", "/me/password", { current_password: current, new_password: next });
      toast.success("Password changed. Your other sessions were logged out.");
      setCurrent("");
      setNext("");
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Change password</CardTitle>
        <CardDescription>The new password needs at least 10 characters.</CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="cur">Current password</Label>
            <Input id="cur" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="new">New password</Label>
            <Input id="new" type="password" autoComplete="new-password" minLength={10} required value={next} onChange={(e) => setNext(e.target.value)} />
          </div>
          {error && <ErrorNote message={error} />}
          <Button type="submit" disabled={busy}>Change password</Button>
        </form>
      </CardContent>
    </Card>
  );
}

function TokensCard() {
  const tokens = useApi<ApiToken[]>("/tokens");
  const [name, setName] = useState("");
  const [created, setCreated] = useState<string | null>(null);

  async function create(e: FormEvent) {
    e.preventDefault();
    try {
      const t = await api<ApiToken & { token: string }>("POST", "/tokens", { name: name.trim() });
      setCreated(t?.token ?? null);
      setName("");
      tokens.reload();
    } catch (err) {
      toast.error(errorText(err));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>API tokens</CardTitle>
        <CardDescription>Let your own scripts use openFRP. A token is shown only once.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <form onSubmit={create} className="flex gap-2">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. home script" maxLength={64} required />
          <Button type="submit" variant="outline"><KeyRound />Create</Button>
        </form>
        {created && (
          <div className="space-y-2 rounded-md border border-warning/40 bg-warning/10 p-3">
            <p className="text-sm font-medium">Copy this token now. It will not be shown again.</p>
            <div className="flex flex-wrap items-center gap-2">
              <code className="break-all font-mono text-sm">{created}</code>
              <CopyButton text={created} />
            </div>
          </div>
        )}
        {tokens.data && tokens.data.length === 0 && <p className="text-sm text-muted-foreground">No API tokens yet.</p>}
        {tokens.data && tokens.data.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow><TableHead>Name</TableHead><TableHead>Starts with</TableHead><TableHead>Created</TableHead><TableHead /></TableRow>
            </TableHeader>
            <TableBody>
              {tokens.data.map((t) => (
                <TableRow key={t.id}>
                  <TableCell>{t.name}</TableCell>
                  <TableCell><code className="font-mono text-xs">{t.prefix}…</code></TableCell>
                  <TableCell className="text-muted-foreground">{ago(t.created_at)}</TableCell>
                  <TableCell className="text-right">
                    <Confirm
                      title="Revoke token?"
                      description="Scripts using this token will stop working."
                      confirmLabel="Revoke"
                      destructive
                      trigger={<Button variant="ghost" size="sm" className="text-destructive">Revoke</Button>}
                      onConfirm={async () => {
                        try {
                          await api("DELETE", `/tokens/${t.id}`);
                          toast.success("Token revoked");
                          tokens.reload();
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
        )}
      </CardContent>
    </Card>
  );
}

function ReservedCard() {
  const rs = useApi<ReservedPort[]>("/reserved-ports");
  return (
    <Card>
      <CardHeader>
        <CardTitle>Reserved ports</CardTitle>
        <CardDescription>Fixed ports the administrator has set aside for you.</CardDescription>
      </CardHeader>
      <CardContent>
        {rs.data && rs.data.length === 0 && (
          <p className="text-sm text-muted-foreground">No reserved ports. Ask the administrator if you need a fixed port.</p>
        )}
        {rs.data && rs.data.length > 0 && (
          <Table>
            <TableHeader><TableRow><TableHead>Port</TableHead><TableHead>Protocol</TableHead><TableHead>Note</TableHead></TableRow></TableHeader>
            <TableBody>
              {rs.data.map((r) => (
                <TableRow key={`${r.type}-${r.remote_port}`}>
                  <TableCell><code className="font-mono">{r.remote_port}</code></TableCell>
                  <TableCell>{r.type.toUpperCase()}</TableCell>
                  <TableCell className="text-muted-foreground">{r.note}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function ActivityCard() {
  const audit = useApi<AuditEntry[]>("/audit");
  return (
    <Card>
      <CardHeader>
        <CardTitle>Recent activity</CardTitle>
        <CardDescription>Logins, tunnel changes and other actions on your account.</CardDescription>
      </CardHeader>
      <CardContent>
        {audit.data && audit.data.length === 0 && <p className="text-sm text-muted-foreground">Nothing yet.</p>}
        {audit.data && audit.data.length > 0 && (
          <Table>
            <TableHeader><TableRow><TableHead>When</TableHead><TableHead>Action</TableHead><TableHead>Target</TableHead></TableRow></TableHeader>
            <TableBody>
              {audit.data.slice(0, 30).map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="text-muted-foreground">{ago(e.at)}</TableCell>
                  <TableCell>{e.action}</TableCell>
                  <TableCell className="text-muted-foreground">{e.target}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
