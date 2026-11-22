import { useState, type ReactNode } from "react";
import { Copy, Inbox } from "lucide-react";
import { toast } from "@/lib/toast";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
      <div className="space-y-1">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </div>
  );
}

export function EmptyState({ title, description, action }: { title: string; description?: string; action?: ReactNode }) {
  return (
    <Card className="border-dashed">
      <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
        <Inbox className="size-8 text-muted-foreground" />
        <div className="space-y-1">
          <p className="font-medium">{title}</p>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {action}
      </CardContent>
    </Card>
  );
}

export function DeviceStatus({ online, paused }: { online: boolean; paused: boolean }) {
  if (paused) return <Badge variant="warning">Paused</Badge>;
  return online ? <Badge variant="success">Online</Badge> : <Badge variant="muted">Offline</Badge>;
}

export function TunnelStatus({ enabled, live }: { enabled: boolean; live: boolean }) {
  if (!enabled) return <Badge variant="muted">Off</Badge>;
  return live ? <Badge variant="success">Live</Badge> : <Badge variant="warning">Waiting</Badge>;
}

export async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success("Copied to clipboard");
  } catch {
    toast.error("Could not copy. Select the text and copy it manually.");
  }
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  return (
    <Button type="button" variant="outline" size="sm" onClick={() => copy(text)}>
      <Copy />
      {label}
    </Button>
  );
}

interface ConfirmProps {
  title: string;
  description: string;
  confirmLabel: string;
  destructive?: boolean;
  trigger: ReactNode;
  onConfirm: () => Promise<boolean> | boolean;
}

/** Replaces window.confirm(): renders its trigger and asks before running onConfirm. */
export function Confirm({ title, description, confirmLabel, destructive, trigger, onConfirm }: ConfirmProps) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  return (
    <>
      <span onClick={() => setOpen(true)} className="contents">
        {trigger}
      </span>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button
              variant={destructive ? "destructive" : "default"}
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                const ok = await onConfirm();
                setBusy(false);
                if (ok) setOpen(false);
              }}
            >
              {confirmLabel}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: string }) {
  return (
    <Card>
      <CardContent className="space-y-1 pt-5">
        <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p>
        <p className="text-2xl font-semibold tabular-nums">{value}</p>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}

export function ErrorNote({ message }: { message: string }) {
  return <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{message}</p>;
}
