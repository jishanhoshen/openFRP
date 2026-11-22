import { useState, type FormEvent } from "react";
import { Cable, Monitor, Plus } from "lucide-react";
import { toast } from "@/lib/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { DeviceStatus, EmptyState, ErrorNote, PageHeader } from "@/components/common";
import { api, errorText, useApi } from "@/lib/api";
import { ago } from "@/lib/format";
import { go } from "@/lib/router";
import type { Device } from "@/lib/types";

export function DevicesPage({ initialCode, openLink }: { initialCode?: string; openLink?: boolean }) {
  const { data, error, loading, reload } = useApi<Device[]>("/devices", 15000);
  const [linkOpen, setLinkOpen] = useState(Boolean(initialCode) || Boolean(openLink));

  return (
    <>
      <PageHeader
        title="Your computers"
        description="Each computer runs the openFRP agent and shares games or apps for you."
        actions={
          <Button onClick={() => setLinkOpen(true)}>
            <Plus />
            Link computer
          </Button>
        }
      />

      {error && <ErrorNote message={error} />}

      {loading && !data && <p className="text-sm text-muted-foreground">Loading your computers…</p>}

      {data && data.length === 0 && <Onboarding onLink={() => setLinkOpen(true)} />}

      {data && data.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          {data.map((d) => (
            <a key={d.id} href={`#/devices/${d.id}`} className="group block">
              <Card className="h-full transition-colors group-hover:border-primary/60">
                <CardHeader className="pb-3">
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex min-w-0 items-center gap-2">
                      <Monitor className="size-4 shrink-0 text-muted-foreground" />
                      <CardTitle className="truncate text-base">{d.name}</CardTitle>
                    </div>
                    <DeviceStatus online={d.online} paused={d.disabled} />
                  </div>
                </CardHeader>
                <CardContent className="space-y-1 text-sm text-muted-foreground">
                  <p>{[d.os, d.arch].filter(Boolean).join(" · ") || "Not connected yet"}</p>
                  <p>Last seen {ago(d.last_seen_at)}</p>
                </CardContent>
              </Card>
            </a>
          ))}
        </div>
      )}

      <LinkDialog open={linkOpen} onOpenChange={setLinkOpen} initialCode={initialCode ?? ""} onLinked={reload} />
    </>
  );
}

function Onboarding({ onLink }: { onLink: () => void }) {
  const steps = [
    { title: "Install the agent", text: "Run the openFRP agent on the computer that runs your game or app." },
    { title: "Link it", text: "Enter the code the agent shows. Each code works for 10 minutes." },
    { title: "Add a tunnel", text: "Pick a preset, then share the address it gives you with friends." },
  ];
  return (
    <EmptyState
      title="No computers yet"
      description="Start with these three steps."
      action={
        <div className="w-full max-w-md space-y-4 pt-2 text-left">
          <ol className="space-y-3">
            {steps.map((s, i) => (
              <li key={s.title} className="flex gap-3">
                <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">
                  {i + 1}
                </span>
                <div>
                  <p className="font-medium">{s.title}</p>
                  <p className="text-sm text-muted-foreground">{s.text}</p>
                </div>
              </li>
            ))}
          </ol>
          <Button onClick={onLink} className="w-full">
            <Cable />
            I have a code, link my computer
          </Button>
        </div>
      }
    />
  );
}

function LinkDialog({
  open,
  onOpenChange,
  initialCode,
  onLinked,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  initialCode: string;
  onLinked: () => void;
}) {
  const [code, setCode] = useState(initialCode);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const d = await api<Device>("POST", "/devices/claim", { code: code.trim(), name: name.trim() });
      toast.success("Computer linked");
      onOpenChange(false);
      onLinked();
      if (d) go(`#/devices/${d.id}`);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Link a computer</DialogTitle>
          <DialogDescription>Enter the code shown by the agent on the computer you want to link.</DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="code">Pairing code</Label>
            <Input id="code" required placeholder="ABCD-EFGH" value={code} maxLength={16}
              className="font-mono uppercase tracking-widest" onChange={(e) => setCode(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label htmlFor="cname">Name (optional)</Label>
            <Input id="cname" placeholder="e.g. Gaming PC" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          {error && <ErrorNote message={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={busy}>{busy ? "Linking…" : "Link computer"}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
