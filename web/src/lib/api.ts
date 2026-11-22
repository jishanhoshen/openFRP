import { useCallback, useEffect, useRef, useState } from "react";

export class ApiError extends Error {
  status: number;
  network: boolean;
  constructor(message: string, status = 0, network = false) {
    super(message);
    this.status = status;
    this.network = network;
  }
}

function friendly(status: number, server?: string): string {
  if (status === 401) return "Your session has ended. Please log in again.";
  if (status === 429) return "Too many attempts. Please wait a minute and try again.";
  if (status === 503) return "The server is busy right now. Please try again shortly.";
  return server || `Something went wrong (HTTP ${status}).`;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T | null> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, credentials: "same-origin", headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  let resp: Response;
  try {
    resp = await fetch("/api/v1" + path, init);
  } catch {
    throw new ApiError("Cannot reach the server. Check your internet connection.", 0, true);
  }
  if (resp.status === 204 || resp.status === 304) return null;
  let data: unknown = null;
  try {
    data = await resp.json();
  } catch {
    data = null;
  }
  if (!resp.ok) {
    const server = (data as { error?: string } | null)?.error;
    if (resp.status === 401 && location.hash !== "#/login") location.hash = "#/login";
    throw new ApiError(friendly(resp.status, server), resp.status);
  }
  return data as T;
}

export function errorText(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong.";
}

/**
 * Loads `path` and optionally refreshes it every `everyMs`.
 * Pass `null` as the path to skip loading. The previous data stays visible
 * while a refresh runs, so pages do not flicker.
 */
export function useApi<T>(path: string | null, everyMs = 0) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(path !== null);
  const alive = useRef(true);

  const load = useCallback(async () => {
    if (path === null) return;
    try {
      const d = await api<T>("GET", path);
      if (alive.current) {
        setData(d);
        setError(null);
      }
    } catch (e) {
      if (alive.current) setError(errorText(e));
    } finally {
      if (alive.current) setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    alive.current = true;
    setLoading(path !== null);
    load();
    if (everyMs > 0) {
      const t = setInterval(() => {
        if (!document.hidden) load();
      }, everyMs);
      return () => {
        alive.current = false;
        clearInterval(t);
      };
    }
    return () => {
      alive.current = false;
    };
  }, [load, everyMs, path]);

  return { data, error, loading, reload: load };
}
