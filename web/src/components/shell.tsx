import type { ReactNode } from "react";
import { Home, LogOut, Monitor, Plus, Settings, Shield, Users, ScrollText, Ticket, Radio } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/lib/auth";
import { useRoute } from "@/lib/router";
import { cn } from "@/lib/utils";

interface NavItem {
  href: string;
  label: string;
  icon: typeof Home;
  match: string;
  sub?: string;
}

const MAIN: NavItem[] = [
  { href: "#/", label: "Overview", icon: Home, match: "" },
  { href: "#/devices", label: "Computers", icon: Monitor, match: "devices" },
];

const ACCOUNT: NavItem[] = [{ href: "#/account", label: "Account", icon: Settings, match: "account" }];

const ADMIN: NavItem[] = [
  { href: "#/admin", label: "Overview", icon: Shield, match: "admin", sub: "" },
  { href: "#/admin/users", label: "Users", icon: Users, match: "admin", sub: "users" },
  { href: "#/admin/ports", label: "Reserved ports", icon: Ticket, match: "admin", sub: "ports" },
  { href: "#/admin/audit", label: "Audit log", icon: ScrollText, match: "admin", sub: "audit" },
];

const TITLES: Record<string, string> = {
  "": "Overview",
  devices: "Computers",
  account: "Account",
  admin: "Administration",
  claim: "Link a computer",
};

function initials(email: string) {
  const name = email.split("@")[0] ?? "";
  return (name.slice(0, 2) || "??").toUpperCase();
}

function NavLink({ item, active, compact }: { item: NavItem; active: boolean; compact?: boolean }) {
  return (
    <a
      href={item.href}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground",
        active && "bg-accent text-accent-foreground",
        compact && "shrink-0 px-2.5 py-1.5",
      )}
    >
      <item.icon className="size-4 shrink-0" />
      {item.label}
    </a>
  );
}

export function Shell({ children }: { children: ReactNode }) {
  const { me, logout } = useAuth();
  const route = useRoute();
  const section = route.parts[0] ?? "";
  const sub = route.parts[1] ?? "";
  const isActive = (item: NavItem) => {
    if (item.match === "admin") return section === "admin" && (item.sub ?? "") === (sub === "" || sub === undefined ? "" : sub);
    return section === item.match;
  };
  const admin = me?.role === "admin";
  const title = TITLES[section] ?? "openFRP";

  return (
    <div className="min-h-screen bg-muted/40">
      <aside className="fixed inset-y-0 left-0 z-20 hidden w-64 flex-col border-r bg-background md:flex">
        <div className="flex h-14 items-center gap-2 px-5">
          <span className="flex size-7 items-center justify-center rounded-md bg-primary text-primary-foreground">
            <Radio className="size-4" />
          </span>
          <a href="#/" className="text-base font-semibold tracking-tight">open<span className="text-primary">FRP</span></a>
        </div>

        <nav className="flex-1 space-y-6 overflow-y-auto px-3 py-2" aria-label="Main">
          <div className="space-y-1">
            <p className="px-3 pb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">Platform</p>
            {MAIN.map((i) => <NavLink key={i.href} item={i} active={isActive(i)} />)}
          </div>
          <div className="space-y-1">
            <p className="px-3 pb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">Account</p>
            {ACCOUNT.map((i) => <NavLink key={i.href} item={i} active={isActive(i)} />)}
          </div>
          {admin && (
            <div className="space-y-1">
              <p className="px-3 pb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">Administration</p>
              {ADMIN.map((i) => <NavLink key={i.href} item={i} active={isActive(i)} />)}
            </div>
          )}
        </nav>

        <div className="border-t p-3">
          <div className="flex items-center gap-3 rounded-md px-2 py-2">
            <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold">
              {initials(me?.email ?? "")}
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium">{me?.email}</p>
              <p className="text-xs text-muted-foreground">{admin ? "Administrator" : "Member"}</p>
            </div>
            <Button variant="ghost" size="icon" onClick={logout} aria-label="Log out"><LogOut /></Button>
          </div>
        </div>
      </aside>

      <div className="md:pl-64">
        <header className="sticky top-0 z-10 border-b bg-background/80 backdrop-blur">
          <div className="flex h-14 items-center justify-between gap-3 px-4 md:px-8">
            <div className="flex min-w-0 items-center gap-3">
              <a href="#/" className="text-base font-semibold md:hidden">open<span className="text-primary">FRP</span></a>
              <h2 className="truncate text-sm font-semibold md:text-base">{title}</h2>
            </div>
            <div className="flex items-center gap-2">
              <Button size="sm" asChild>
                <a href="#/devices?new=1"><Plus />Add computer</a>
              </Button>
              <Button variant="ghost" size="icon" className="md:hidden" onClick={logout} aria-label="Log out"><LogOut /></Button>
            </div>
          </div>
          <nav className="flex gap-1 overflow-x-auto px-4 pb-2 md:hidden" aria-label="Sections">
            {[...MAIN, ...ACCOUNT, ...(admin ? [ADMIN[0]] : [])].map((i) => (
              <NavLink key={i.href} item={i} active={isActive(i)} compact />
            ))}
          </nav>
        </header>
        <main className="mx-auto max-w-6xl px-4 py-6 md:px-8 md:py-8">{children}</main>
      </div>
    </div>
  );
}
