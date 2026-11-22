import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api } from "@/lib/api";
import type { Info, Me } from "@/lib/types";

interface AuthState {
  me: Me | null;
  info: Info | null;
  ready: boolean;
  setMe: (m: Me | null) => void;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [info, setInfo] = useState<Info | null>(null);
  const [ready, setReady] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setMe(await api<Me>("GET", "/me"));
    } catch {
      setMe(null);
    }
    try {
      setInfo(await api<Info>("GET", "/info"));
    } catch {
      setInfo(null);
    }
    setReady(true);
  }, []);

  const logout = useCallback(async () => {
    try {
      await api("POST", "/auth/logout");
    } catch {
      /* the session cookie is cleared locally below regardless */
    }
    setMe(null);
    location.hash = "#/login";
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return (
    <AuthContext.Provider value={{ me, info, ready, setMe, refresh, logout }}>{children}</AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside AuthProvider");
  return ctx;
}
