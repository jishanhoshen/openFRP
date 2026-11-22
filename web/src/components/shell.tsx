import type { ReactNode } from "react";
import { LayoutDashboard, LogOut, Monitor, Settings, Shield } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/lib/auth";
import { useRoute } from "@/lib/router";
import { cn } from "@/lib/utils";

const NAV = [
  { href: "#/devices", label: "Computers", icon: Monitor, match: "devices" },
  { href: "#/account", label: "Account", icon: Settings, match: "account" },
];

export function Shell({ children }: { children: ReactNode }) {
  const { me, logout } = useAuth();
  const route = useRoute();
  const current = route.parts[0] ?? "";
  const items = me?.role === "admin"
    ? [...NAV, { href: "#/admin", label: "Admin", icon: Shield, match: "admin" }]
    : NAV;

  return (
    <div className="min-h-screen bg-muted/30">
      <aside className="fixed inset-y-0 left-0 z-20 hidden w-60 flex-col border-r bg-background md:flex">
        <div className="flex h-14 items-center gap-2 border-b px-5">
          <LayoutDashboard className="size-5 text-primary" />
          <a href="#/devices" className="text-base font-semibold">
            open<span className="text-primary">FRP</span>
          </a>
        </div>
        <nav className="flex flex-1 flex-col gap-1 p-3">
          {items.map((i) => (
            <a
              key={i.href}
              href={i.href}
              className={cn(
                "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
                current === i.match && "bg-accent text-accent-foreground",
              )}
            >
              <i.icon className="size-4" />
              {i.label}
            </a>
          ))}
        </nav>
        <div className="border-t p-3">
          <p className="truncate px-3 pb-2 text-xs text-muted-foreground">{me?.email}</p>
          <Button variant="ghost" className="w-full justify-start" onClick={logout}>
            <LogOut />
            Log out
          </Button>
        </div>
      </aside>

      <div className="md:pl-60">
        <header className="sticky top-0 z-10 flex h-14 items-center justify-between gap-3 border-b bg-background/80 px-4 backdrop-blur md:hidden">
          <a href="#/devices" className="text-base font-semibold">
            open<span className="text-primary">FRP</span>
          </a>
          <div className="flex items-center gap-1">
            {items.map((i) => (
              <a
                key={i.href}
                href={i.href}
                aria-label={i.label}
                className={cn("rounded-md p-2 text-muted-foreground", current === i.match && "bg-accent text-foreground")}
              >
                <i.icon className="size-4" />
              </a>
            ))}
            <Button variant="ghost" size="icon" onClick={logout} aria-label="Log out">
              <LogOut />
            </Button>
          </div>
        </header>
        <main className="mx-auto max-w-5xl px-4 py-6 md:px-8 md:py-8">{children}</main>
      </div>
    </div>
  );
}
