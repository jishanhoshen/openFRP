import { useEffect, useState } from "react";

export interface Route {
  parts: string[];
  query: URLSearchParams;
}

function read(): Route {
  const raw = location.hash.replace(/^#/, "") || "/";
  const [path, q] = raw.split("?");
  const parts = path
    .split("/")
    .filter(Boolean)
    .map((p) => decodeURIComponent(p));
  return { parts, query: new URLSearchParams(q || "") };
}

/** Hash-based routing: the server only needs to serve the single app page. */
export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(read);
  useEffect(() => {
    const on = () => setRoute(read());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}

export function go(to: string) {
  location.hash = to.startsWith("#") ? to : "#" + to;
}
