import type { ReactNode } from "react";
import { Toaster } from "@/components/toaster";
import { AuthProvider, useAuth } from "@/lib/auth";
import { useRoute, go } from "@/lib/router";
import { Shell } from "@/components/shell";
import { EmptyState } from "@/components/common";
import { LoginPage } from "@/pages/login";
import { DevicesPage } from "@/pages/devices";
import { DevicePage } from "@/pages/device";
import { TunnelPage } from "@/pages/tunnel";
import { AccountPage } from "@/pages/account";
import { AdminPage } from "@/pages/admin";

function Router() {
  const { me, info, ready } = useAuth();
  const route = useRoute();
  const [first = "", second = "", third = "", fourth = ""] = route.parts;

  if (!ready) return <div className="flex min-h-screen items-center justify-center text-sm text-muted-foreground">Loading…</div>;

  // Public pages
  if (first === "login" || (first === "register" && info?.open_registration)) {
    if (me) {
      go("#/devices");
      return null;
    }
    return <LoginPage register={first === "register"} />;
  }

  if (!me) {
    go("#/login");
    return null;
  }

  let page: ReactNode;
  switch (first) {
    case "":
    case "devices":
      if (!second) page = <DevicesPage />;
      else if (third === "tunnels" && fourth) page = <TunnelPage deviceId={second} tunnelId={fourth} />;
      else page = <DevicePage id={second} />;
      break;
    case "claim":
      page = <DevicesPage initialCode={route.query.get("code") ?? ""} />;
      break;
    case "account":
      page = <AccountPage />;
      break;
    case "admin":
      page = <AdminPage tab={second} />;
      break;
    default:
      page = <EmptyState title="Page not found" description="The link may be old or mistyped." action={<a className="text-sm text-primary hover:underline" href="#/devices">Go to your computers</a>} />;
  }

  return <Shell>{page}</Shell>;
}

export default function App() {
  return (
    <AuthProvider>
      <Router />
      <Toaster />
    </AuthProvider>
  );
}
