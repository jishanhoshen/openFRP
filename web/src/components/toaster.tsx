import { useSyncExternalStore } from "react";
import { CheckCircle2, CircleAlert, Info } from "lucide-react";
import { currentToasts, subscribeToasts, type ToastKind } from "@/lib/toast";
import { cn } from "@/lib/utils";

const STYLE: Record<ToastKind, { icon: typeof Info; cls: string }> = {
  success: { icon: CheckCircle2, cls: "text-success" },
  error: { icon: CircleAlert, cls: "text-destructive" },
  info: { icon: Info, cls: "text-primary" },
};

export function Toaster() {
  const list = useSyncExternalStore(subscribeToasts, currentToasts, currentToasts);
  return (
    <div role="status" aria-live="polite" className="pointer-events-none fixed bottom-4 right-4 z-[60] flex w-[calc(100%-2rem)] max-w-sm flex-col gap-2">
      {list.map((t) => {
        const { icon: Icon, cls } = STYLE[t.kind];
        return (
          <div key={t.id} className="pointer-events-auto flex items-start gap-3 rounded-lg border bg-background p-4 text-sm shadow-lg">
            <Icon className={cn("mt-0.5 size-4 shrink-0", cls)} />
            <p className="leading-snug">{t.message}</p>
          </div>
        );
      })}
    </div>
  );
}
