// openFRP dashboard.
// Built with DOM APIs only (no innerHTML): device names and other user text are
// never parsed as markup. No inline script or style (see the CSP in the server).
(() => {
  "use strict";

  const app = document.getElementById("app");
  const nav = document.getElementById("nav");
  const who = document.getElementById("who");

  let me = null;
  let info = {};
  let timers = [];

  // Presets for the games and servers people most often share.
  const PRESETS = [
    { id: "mc-java", label: "Minecraft (Java)", type: "tcp", port: 25565, name: "minecraft",
      hint: "Players type only your address. Minecraft's default port is 25565." },
    { id: "mc-bedrock", label: "Minecraft (Bedrock)", type: "udp", port: 19132, name: "minecraft-bedrock",
      hint: "Bedrock and Pocket Edition use UDP on port 19132." },
    { id: "udp-game", label: "UDP game", type: "udp", port: 26001, name: "game",
      hint: "Use the local port your game server listens on." },
    { id: "tcp-app", label: "Web or TCP app", type: "tcp", port: 8080, name: "app",
      hint: "Any TCP service running on this computer." },
    { id: "custom", label: "Custom", type: "tcp", port: "", name: "",
      hint: "Choose your own protocol and port." },
  ];

  const NAME_RE = /^[a-z0-9][a-z0-9-]{0,47}$/;

  // ------------------------------------------------------------ helpers

  function el(tag, attrs, ...kids) {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === undefined || v === null || v === false) continue;
      if (k === "class") n.className = v;
      else if (k === "text") n.textContent = v;
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v === true ? "" : String(v));
    }
    for (const kid of kids.flat()) {
      if (kid === null || kid === undefined || kid === false) continue;
      n.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
    }
    return n;
  }

  function setView(...nodes) {
    stopTimers();
    app.replaceChildren(...nodes.flat());
  }

  function every(ms, fn) {
    timers.push(setInterval(() => { if (!document.hidden) fn(); }, ms));
  }

  function stopTimers() {
    timers.forEach(clearInterval);
    timers = [];
  }

  // Short, readable error messages.
  function friendly(err) {
    if (err && err.network) return "Cannot reach the server. Check your internet connection.";
    if (err && err.status === 401) return "Your session has ended. Please log in again.";
    if (err && err.status === 429) return "Too many attempts. Please wait a minute and try again.";
    if (err && err.status === 503) return "The server is busy right now. Please try again shortly.";
    return (err && err.message) || "Something went wrong.";
  }

  async function api(method, path, body) {
    const opt = { method, credentials: "same-origin", headers: {} };
    if (body !== undefined) {
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    let resp;
    try {
      resp = await fetch("/api/v1" + path, opt);
    } catch (_) {
      const err = new Error("network");
      err.network = true;
      throw err;
    }
    if (resp.status === 204 || resp.status === 304) return null;
    let data = null;
    try { data = await resp.json(); } catch (_) { /* empty body */ }
    if (!resp.ok) {
      const err = new Error((data && data.error) || `HTTP ${resp.status}`);
      err.status = resp.status;
      throw err;
    }
    return data;
  }

  // Run an API call; on failure show a toast and return undefined.
  async function act(fn, okMsg) {
    try {
      const r = await fn();
      if (okMsg) toast(okMsg, "ok");
      return r === null ? true : r;
    } catch (e) {
      toast(friendly(e), "error");
      if (e.status === 401) {
        me = null;
        location.hash = "#/login";
      }
      return undefined;
    }
  }

  function toast(msg, kind = "info") {
    let box = document.getElementById("toasts");
    if (!box) {
      box = el("div", { id: "toasts", class: "toasts", role: "status", "aria-live": "polite" });
      document.body.append(box);
    }
    const t = el("div", { class: "toast " + kind, text: msg });
    box.append(t);
    setTimeout(() => t.remove(), kind === "error" ? 6000 : 3000);
  }

  // In-app confirmation (replaces window.confirm). Resolves to true or false.
  function confirmBox(title, message, okLabel = "Confirm", danger = false) {
    return new Promise((resolve) => {
      const dlg = el("dialog", { class: "confirm" },
        el("h2", { text: title }),
        el("p", { text: message }),
        el("div", { class: "actions" },
          el("button", { type: "button", onclick: () => dlg.close("cancel"), text: "Cancel" }),
          el("button", { type: "button", class: danger ? "danger strong" : "primary",
            onclick: () => dlg.close("ok"), text: okLabel })));
      dlg.addEventListener("close", () => {
        resolve(dlg.returnValue === "ok");
        dlg.remove();
      });
      document.body.append(dlg);
      dlg.showModal();
    });
  }

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast("Copied to clipboard", "ok");
    } catch (_) {
      const ta = el("textarea", { class: "offscreen" });
      ta.value = text;
      document.body.append(ta);
      ta.select();
      const ok = document.execCommand("copy");
      ta.remove();
      toast(ok ? "Copied to clipboard" : "Could not copy. Select the text and copy it manually.", ok ? "ok" : "error");
    }
  }

  function copyButton(text, label = "Copy") {
    return el("button", { type: "button", class: "small", onclick: () => copyText(text), text: label });
  }

  function fmtBytes(n) {
    if (n === null || n === undefined) return "–";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0, v = Number(n);
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(v < 10 ? 2 : 1)) + " " + units[i];
  }

  // "3 min ago", with the exact time as a tooltip.
  function ago(s) {
    if (!s) return { text: "never", title: "" };
    const d = new Date(s);
    if (isNaN(d)) return { text: s, title: "" };
    const sec = Math.max(0, (Date.now() - d.getTime()) / 1000);
    let text;
    if (sec < 60) text = "just now";
    else if (sec < 3600) text = Math.floor(sec / 60) + " min ago";
    else if (sec < 86400) text = Math.floor(sec / 3600) + " h ago";
    else text = Math.floor(sec / 86400) + " d ago";
    return { text, title: d.toLocaleString() };
  }

  function timeEl(s) {
    const a = ago(s);
    return el("span", { title: a.title, text: a.text });
  }

  function badge(text, kind) {
    return el("span", { class: "badge " + (kind || "") }, text);
  }

  function button(label, onclick, opts = {}) {
    const cls = [opts.primary ? "primary" : "", opts.danger ? "danger" : "", opts.small ? "small" : ""]
      .join(" ").trim();
    return el("button", { type: "button", class: cls, onclick, disabled: opts.disabled, text: label });
  }

  function field(label, control, hint) {
    return el("label", { class: "field" }, el("span", { text: label }), control,
      hint ? el("small", { class: "muted", text: hint }) : null);
  }

  function input(attrs) {
    return el("input", attrs);
  }

  function table(headers, rows) {
    return el("div", { class: "scroll" },
      el("table", null,
        el("thead", null, el("tr", null, headers.map(h => el("th", { class: h.num ? "num" : "", text: h.label || h })))),
        el("tbody", null, rows)));
  }

  function empty(title, text, action) {
    return el("div", { class: "empty" },
      el("h3", { text: title }),
      text ? el("p", { class: "muted", text }) : null,
      action || null);
  }

  function errorNote(msg) {
    return el("p", { class: "error", text: msg });
  }

  function setNav(items) {
    nav.replaceChildren(...items.map(([label, href, active]) =>
      el("a", { href, class: active ? "active" : "", text: label })));
  }

  function statCard(value, label) {
    return el("div", { class: "stat" },
      el("div", { class: "v", text: String(value) }),
      el("div", { class: "l", text: label }));
  }

  // Renders the result of an async builder into a node that fills in when ready.
  function placeholder(build) {
    const holder = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    build().then(node => holder.replaceChildren(node));
    return holder;
  }

  // ------------------------------------------------------------ session

  async function loadSession() {
    try { me = await api("GET", "/me"); } catch (_) { me = null; }
    try { info = (await api("GET", "/info")) || {}; } catch (_) { info = {}; }
    who.replaceChildren();
    if (me) {
      who.append(el("span", { class: "muted", text: me.email }),
        me.role === "admin" ? badge("admin", "info") : null,
        el("button", { type: "button", class: "small", onclick: logout, text: "Log out" }));
    }
  }

  async function logout() {
    await act(() => api("POST", "/auth/logout"));
    me = null;
    location.hash = "#/login";
    route();
  }

  function requireLogin() {
    if (!me) {
      location.hash = "#/login";
      return false;
    }
    return true;
  }

  function baseNav(active) {
    const items = [["Computers", "#/devices", active === "devices"], ["Account", "#/account", active === "account"]];
    if (me && me.role === "admin") items.push(["Admin", "#/admin", active === "admin"]);
    setNav(items);
  }

  // ------------------------------------------------------------ auth

  function loginPage(register) {
    const email = input({ type: "email", name: "email", required: true, autocomplete: "username", maxlength: 254 });
    const pw = input({ type: "password", name: "password", required: true,
      autocomplete: register ? "new-password" : "current-password", minlength: 10, maxlength: 256 });
    const msg = el("p", { class: "error", role: "alert" });
    const submit = el("button", { type: "submit", class: "primary wide", text: register ? "Create account" : "Log in" });
    const form = el("form", { class: "card narrow", onsubmit: async (ev) => {
      ev.preventDefault();
      msg.textContent = "";
      submit.disabled = true;
      try {
        if (register) await api("POST", "/auth/register", { email: email.value, password: pw.value });
        await api("POST", "/auth/login", { email: email.value, password: pw.value });
        await loadSession();
        location.hash = "#/devices";
        route();
      } catch (e) {
        msg.textContent = friendly(e);
      } finally {
        submit.disabled = false;
      }
    } },
      el("h1", { text: register ? "Create your account" : "Welcome back" }),
      el("p", { class: "muted", text: register
        ? "Share games from your own computer, through your own public address."
        : "Log in to manage your computers and tunnels." }),
      field("Email", email),
      field("Password", pw, register ? "At least 10 characters." : null),
      submit, msg,
      el("p", { class: "muted center" }, register
        ? [el("a", { href: "#/login", text: "Already have an account? Log in" })]
        : [info.open_registration
          ? el("a", { href: "#/register", text: "Create an account" })
          : el("span", { text: "No account? Ask the administrator to create one." })]));
    setNav([]);
    setView(el("div", { class: "center-page" }, form));
  }

  // ------------------------------------------------------------ computers (devices)

  async function devicesPage() {
    if (!requireLogin()) return;
    baseNav("devices");
    const list = el("div", null, el("p", { class: "muted", text: "Loading your computers…" }));
    setView(
      el("div", { class: "page-head" },
        el("h1", { text: "Your computers" }),
        el("span", { class: "muted", text: "Each computer runs the agent and hosts games or apps for you." })),
      list,
      el("details", { class: "card link-box" },
        el("summary", { text: "Link a new computer" }),
        el("p", { class: "muted", text: "Start the agent on the computer, then enter the code it shows." }),
        claimForm()));

    const refresh = async () => {
      const ds = await act(() => api("GET", "/devices"));
      if (ds === undefined) return;
      if (ds.length === 0) {
        list.replaceChildren(onboarding());
        return;
      }
      list.replaceChildren(el("div", { class: "grid cards" }, ds.map(deviceCard)));
    };
    await refresh();
    every(15000, refresh);
  }

  function onboarding() {
    return el("div", { class: "card onboard" },
      el("h2", { text: "Get started in three steps" }),
      el("ol", null,
        el("li", null, el("strong", { text: "Install the agent " }),
          el("span", { text: "on the computer that runs your game or app." })),
        el("li", null, el("strong", { text: "Link it " }),
          el("span", { text: "by entering the code the agent shows, under \"Link a new computer\" below." })),
        el("li", null, el("strong", { text: "Add a tunnel " }),
          el("span", { text: "and share the address it gives you with your friends." }))));
  }

  function deviceCard(d) {
    const status = d.disabled ? badge("paused", "warn") : d.online ? badge("online", "good") : badge("offline", "");
    return el("a", { class: "card device-card", href: "#/devices/" + d.id },
      el("div", { class: "row between" }, el("strong", { text: d.name }), status),
      el("p", { class: "muted small-text", text: [d.os, d.arch].filter(Boolean).join(" · ") || "Not connected yet" }),
      el("p", { class: "muted small-text" }, "Last seen ", timeEl(d.last_seen_at)));
  }

  function claimForm(initialCode) {
    const code = input({ name: "code", required: true, placeholder: "ABCD-EFGH", value: initialCode || "",
      autocomplete: "off", maxlength: 16, class: "code-input", "aria-label": "Pairing code" });
    const name = input({ name: "name", placeholder: "e.g. Gaming PC", maxlength: 64, "aria-label": "Computer name" });
    const msg = el("p", { class: "error", role: "status" });
    return el("form", { class: "row wrap", onsubmit: async (ev) => {
      ev.preventDefault();
      msg.textContent = "";
      const d = await act(() => api("POST", "/devices/claim", { code: code.value.trim(), name: name.value.trim() }),
        "Computer linked");
      if (d) location.hash = "#/devices/" + d.id;
      else msg.textContent = "Check the code and try again. Codes expire after 10 minutes.";
    } },
      field("Pairing code", code, "Shown by the agent"),
      field("Name", name, "Optional"),
      el("button", { type: "submit", class: "primary", text: "Link computer" }), msg);
  }

  async function claimPage(params) {
    if (!requireLogin()) return;
    baseNav("devices");
    setView(el("div", { class: "card narrow" },
      el("h1", { text: "Link a computer" }),
      el("p", { class: "muted", text: "Confirm the code shown by the agent to link that computer to your account." }),
      claimForm(params.get("code") || "")));
  }

  // ------------------------------------------------------------ computer page

  async function devicePage(id) {
    if (!requireLogin()) return;
    baseNav("devices");
    const body = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    setView(body);

    let loaded = false;
    const render = async () => {
      const d = await act(() => api("GET", "/devices/" + encodeURIComponent(id)));
      if (!d) {
        if (!loaded) body.replaceChildren(empty("Computer not found", "It may have been removed, or the link is wrong.",
          el("a", { href: "#/devices", text: "Back to your computers" })));
        return;
      }
      loaded = true;
      const tunnels = (await act(() => api("GET", `/devices/${id}/tunnels`))) || [];
      body.replaceChildren(...deviceView(d, tunnels, render));
    };
    await render();
    every(10000, render);
  }

  function deviceView(d, tunnels, reload) {
    const status = d.disabled ? badge("paused", "warn") : d.online ? badge("online", "good") : badge("offline", "");
    const shared = tunnels.filter(t => t.enabled);
    const nextStep = tunnels.length === 0
      ? "Next: add a tunnel below, then share its address with your friends."
      : shared.length === 0
        ? "All tunnels are turned off. Turn one on to share it."
        : d.disabled
          ? "Tunnels are paused. Resume them in the settings below."
          : d.online
            ? "Ready. Share an address below with your friends."
            : "This computer is offline. Start the agent on it to go live.";

    const blocks = [
      el("div", { class: "page-head" },
        el("div", null,
          el("h1", { text: d.name }),
          el("p", { class: "muted", text: [d.hostname, d.os, d.arch, d.agent_version].filter(Boolean).join(" · ") })),
        status),
      el("div", { class: "card hint", text: nextStep }),
    ];

    if (shared.length > 0) {
      blocks.push(el("div", { class: "card" },
        el("h2", { text: "Share with your friends" }),
        el("p", { class: "muted", text: "Give them one of these addresses. They do not need to install anything." }),
        el("ul", { class: "address-list" }, shared.map(t =>
          el("li", null,
            el("div", null, el("strong", { text: t.name }), " ", badge(t.type.toUpperCase(), ""), " ",
              t.live && t.live.online ? badge("live", "good") : badge("waiting", "")),
            el("div", { class: "row wrap" },
              el("code", { class: "address", text: t.public_address }),
              copyButton(t.public_address)))))));
    }

    blocks.push(el("h2", { text: "Tunnels" }));
    blocks.push(tunnels.length === 0
      ? empty("No tunnels yet", "A tunnel opens one public address for one service on this computer.", null)
      : table(["Name", "Address", "Local", "Status", { label: "Today", num: true }, { label: "", num: false }],
        tunnels.map(t => tunnelRow(d.id, t, reload))));

    blocks.push(addTunnelCard(d.id, reload));

    blocks.push(el("details", { class: "card" },
      el("summary", { text: "Computer settings" }),
      renameForm(d, reload),
      el("p", { class: "actions-row" },
        button(d.disabled ? "Resume tunnels" : "Pause tunnels", async () => {
          const ok = await confirmBox(d.disabled ? "Resume tunnels?" : "Pause tunnels?",
            d.disabled ? "Players will be able to connect again." : "Players will be disconnected until you resume.",
            d.disabled ? "Resume" : "Pause");
          if (!ok) return;
          if (await act(() => api("PATCH", "/devices/" + d.id, { disabled: !d.disabled }),
            d.disabled ? "Tunnels resumed" : "Tunnels paused")) reload();
        }),
        button("Remove computer", async () => {
          const ok = await confirmBox("Remove this computer?",
            "Its tunnels are closed and its addresses are released. The agent cannot reconnect afterwards.",
            "Remove", true);
          if (!ok) return;
          if (await act(() => api("DELETE", "/devices/" + d.id), "Computer removed")) location.hash = "#/devices";
        }, { danger: true }))));
    return blocks;
  }

  function tunnelRow(deviceID, t, reload) {
    const status = !t.enabled ? badge("off", "")
      : t.live && t.live.online ? badge("live", "good") : badge("waiting", "warn");
    return el("tr", null,
      el("td", null,
        el("a", { href: `#/devices/${deviceID}/tunnels/${t.id}`, text: t.name }),
        el("div", { class: "muted small-text", text: t.type.toUpperCase() + (t.proxy_protocol ? " · PROXY " + t.proxy_protocol : "") })),
      el("td", null, el("code", { text: t.public_address })),
      el("td", { class: "muted", text: `${t.local_ip}:${t.local_port}` }),
      el("td", null, status),
      el("td", { class: "num", text: t.live ? fmtBytes(t.live.today_bytes_in + t.live.today_bytes_out) : "–" }),
      el("td", { class: "actions-cell" },
        button(t.enabled ? "Turn off" : "Turn on", async () => {
          if (await act(() => api("PATCH", `/devices/${deviceID}/tunnels/${t.id}`, { enabled: !t.enabled }),
            t.enabled ? "Tunnel turned off" : "Tunnel turned on")) reload();
        }, { small: true }),
        button("Delete", async () => {
          const ok = await confirmBox("Delete tunnel?",
            `"${t.name}" will stop. Its address (${t.public_address}) is kept for a short time before anyone else can get it.`,
            "Delete", true);
          if (!ok) return;
          if (await act(() => api("DELETE", `/devices/${deviceID}/tunnels/${t.id}`), "Tunnel deleted")) reload();
        }, { small: true, danger: true })));
  }

  function addTunnelCard(deviceID, reload) {
    let preset = PRESETS[0];
    const typeSel = el("select", { name: "type" },
      el("option", { value: "tcp", text: "TCP" }), el("option", { value: "udp", text: "UDP" }));
    const lport = input({ name: "local_port", type: "number", min: 1, max: 65535, required: true, placeholder: "25565" });
    const name = input({ name: "name", required: true, pattern: "[a-z0-9][a-z0-9-]{0,47}", maxlength: 48, placeholder: "minecraft" });
    const lip = input({ name: "local_ip", value: "127.0.0.1", maxlength: 45 });
    const proxy = el("select", { name: "proxy_protocol" },
      el("option", { value: "", text: "Off" }),
      el("option", { value: "v2", text: "PROXY v2" }),
      el("option", { value: "v1", text: "PROXY v1" }));
    const hint = el("p", { class: "muted small-text", text: preset.hint });
    const chips = el("div", { class: "chips", role: "group", "aria-label": "Presets" });

    const applyPreset = (p) => {
      preset = p;
      typeSel.value = p.type;
      lport.value = p.port;
      name.value = p.name;
      hint.textContent = p.hint;
      chips.querySelectorAll(".chip").forEach(c => c.classList.toggle("on", c.dataset.id === p.id));
    };
    chips.append(...PRESETS.map(p => el("button", { type: "button", class: "chip", "data-id": p.id, text: p.label,
      onclick: () => applyPreset(p) })));
    applyPreset(preset);

    const msg = el("p", { class: "error", role: "status" });
    return el("div", { class: "card" },
      el("h2", { text: "Add a tunnel" }),
      chips, hint,
      el("form", { class: "grid form-grid", onsubmit: async (ev) => {
        ev.preventDefault();
        msg.textContent = "";
        if (!NAME_RE.test(name.value)) {
          msg.textContent = "Name: use lower-case letters, numbers and dashes, for example minecraft.";
          return;
        }
        const body = { name: name.value, type: typeSel.value, local_port: Number(lport.value), remote_port: 0 };
        if (lip.value.trim()) body.local_ip = lip.value.trim();
        if (typeSel.value === "tcp" && proxy.value) body.proxy_protocol = proxy.value;
        const t = await act(() => api("POST", `/devices/${deviceID}/tunnels`, body), "Tunnel created");
        if (t) reload();
        else msg.textContent = "The tunnel could not be created. Try a different name, or check the port.";
      } },
        field("Name", name),
        field("Protocol", typeSel),
        field("Local port", lport, "The port your game or app uses"),
        el("details", { class: "advanced span-all" },
          el("summary", { text: "Advanced" }),
          field("Local address", lip, "Keep 127.0.0.1 if the service runs on this computer."),
          field("Send player IP to the server (TCP only)", proxy, "Only for servers that understand the PROXY protocol.")),
        el("div", { class: "span-all actions-row" },
          el("button", { type: "submit", class: "primary", text: "Create tunnel" })),
        el("div", { class: "span-all" }, msg)));
  }

  function renameForm(d, reload) {
    const n = input({ value: d.name, maxlength: 64, "aria-label": "Computer name" });
    return el("form", { class: "row wrap", onsubmit: async (ev) => {
      ev.preventDefault();
      if (await act(() => api("PATCH", "/devices/" + d.id, { name: n.value.trim() }), "Name saved")) reload();
    } }, field("Name", n), el("button", { type: "submit", text: "Save name" }));
  }

  // ------------------------------------------------------------ tunnel page

  async function tunnelPage(deviceID, tid) {
    if (!requireLogin()) return;
    baseNav("devices");
    const body = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    setView(body);

    let loaded = false;
    const render = async () => {
      const [t, days] = await Promise.all([
        act(() => api("GET", `/devices/${deviceID}/tunnels/${tid}`)),
        act(() => api("GET", `/devices/${deviceID}/tunnels/${tid}/traffic?days=30`)),
      ]);
      if (!t) {
        if (!loaded) body.replaceChildren(empty("Tunnel not found", "It may have been deleted.",
          el("a", { href: "#/devices/" + deviceID, text: "Back to the computer" })));
        return;
      }
      loaded = true;
      const series = days ? days.days : [];
      const total = series.reduce((a, d) => ({ i: a.i + d.bytes_in, o: a.o + d.bytes_out }), { i: 0, o: 0 });
      const live = t.live && t.live.online;
      body.replaceChildren(
        el("p", null, el("a", { href: "#/devices/" + deviceID, text: "← Back to the computer" })),
        el("div", { class: "page-head" },
          el("h1", { text: t.name }),
          !t.enabled ? badge("off", "") : live ? badge("live", "good") : badge("waiting", "warn")),
        el("div", { class: "card address-card" },
          el("span", { class: "muted", text: "Share this address" }),
          el("div", { class: "row wrap" },
            el("code", { class: "address big", text: t.public_address }),
            copyButton(t.public_address, "Copy address")),
          el("p", { class: "muted small-text", text: t.type === "udp"
            ? "UDP address. Players enter it in the game's connect screen."
            : "TCP address. Players connect with host:port." })),
        el("div", { class: "grid stats" },
          statCard(fmtBytes(total.i), "Received, 30 days"),
          statCard(fmtBytes(total.o), "Sent, 30 days"),
          statCard(live ? "Yes" : "No", "Players connected now"),
          statCard(t.live ? t.live.connections : 0, "Open connections")),
        el("h2", { text: "Daily traffic" }),
        series.length === 0
          ? empty("No traffic yet", "Traffic appears here once players connect.", null)
          : table(["Day (UTC)", { label: "In", num: true }, { label: "Out", num: true }],
            series.slice().reverse().map(d => el("tr", null,
              el("td", { text: d.day }), el("td", { class: "num", text: fmtBytes(d.bytes_in) }),
              el("td", { class: "num", text: fmtBytes(d.bytes_out) })))),
        el("p", { class: "muted small-text", text: `Local target: ${t.local_ip}:${t.local_port} · Protocol: ${t.type.toUpperCase()}` }));
    };
    await render();
    every(10000, render);
  }

  // ------------------------------------------------------------ account

  function reservedBox() {
    return placeholder(async () => {
      const rs = await act(() => api("GET", "/reserved-ports"));
      if (rs === undefined) return errorNote("Could not load reserved ports.");
      if (rs.length === 0) return el("p", { class: "muted", text: "You have no reserved ports. Ask the administrator if you need a fixed port." });
      return table(["Port", "Protocol", "Note"], rs.map(r => el("tr", null,
        el("td", null, el("code", { text: String(r.remote_port) })),
        el("td", { text: r.type.toUpperCase() }),
        el("td", { class: "muted", text: r.note }))));
    });
  }

  function activityBox() {
    return placeholder(async () => {
      const es = await act(() => api("GET", "/audit"));
      if (es === undefined) return errorNote("Could not load activity.");
      if (es.length === 0) return el("p", { class: "muted", text: "Nothing yet." });
      return table(["When", "Action", "Target"], es.slice(0, 30).map(e => el("tr", null,
        el("td", null, timeEl(e.at)), el("td", { text: e.action }), el("td", { class: "muted", text: e.target }))));
    });
  }

  async function accountPage() {
    if (!requireLogin()) return;
    baseNav("account");
    const tokenBox = el("div");
    const tokensBox = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    const pwMsg = el("p", { class: "error", role: "status" });
    const cur = input({ type: "password", autocomplete: "current-password", required: true });
    const next = input({ type: "password", autocomplete: "new-password", minlength: 10, required: true });
    const nameIn = input({ maxlength: 64, required: true, placeholder: "e.g. home script" });

    const loadTokens = async () => {
      const ts = await act(() => api("GET", "/tokens"));
      if (ts === undefined) return;
      tokensBox.replaceChildren(ts.length === 0
        ? el("p", { class: "muted", text: "No API tokens yet." })
        : table(["Name", "Starts with", "Created", ""], ts.map(t => el("tr", null,
          el("td", { text: t.name }),
          el("td", null, el("code", { text: t.prefix + "…" })),
          el("td", null, timeEl(t.created_at)),
          el("td", null, button("Revoke", async () => {
            const ok = await confirmBox("Revoke token?", "Scripts using this token will stop working.", "Revoke", true);
            if (ok && await act(() => api("DELETE", "/tokens/" + t.id), "Token revoked")) loadTokens();
          }, { small: true, danger: true }))))));
    };

    setView(
      el("h1", { text: "Account" }),
      el("p", { class: "muted", text: "Signed in as " + me.email }),
      el("div", { class: "card" },
        el("h2", { text: "Change password" }),
        el("form", { class: "row wrap", onsubmit: async (ev) => {
          ev.preventDefault();
          pwMsg.textContent = "";
          if (await act(() => api("POST", "/me/password", { current_password: cur.value, new_password: next.value }),
            "Password changed")) {
            pwMsg.className = "ok";
            pwMsg.textContent = "Done. Your other sessions were logged out.";
            cur.value = ""; next.value = "";
          } else {
            pwMsg.className = "error";
            pwMsg.textContent = "Check your current password. The new one needs at least 10 characters.";
          }
        } },
          field("Current password", cur),
          field("New password", next),
          el("button", { type: "submit", text: "Change" })),
        pwMsg),
      el("div", { class: "card" },
        el("h2", { text: "API tokens" }),
        el("p", { class: "muted", text: "Tokens let your own scripts use openFRP. A token is shown only once, so copy it straight away." }),
        el("form", { class: "row wrap", onsubmit: async (ev) => {
          ev.preventDefault();
          const t = await act(() => api("POST", "/tokens", { name: nameIn.value.trim() }), "Token created");
          if (t) {
            tokenBox.replaceChildren(el("div", { class: "notice" },
              el("p", { text: "Copy this token now. It will not be shown again." }),
              el("div", { class: "row wrap" },
                el("code", { class: "address", text: t.token }),
                copyButton(t.token))));
            nameIn.value = "";
            loadTokens();
          }
        } },
          field("Name", nameIn),
          el("button", { type: "submit", text: "Create token" })),
        tokenBox, tokensBox),
      el("div", { class: "card" }, el("h2", { text: "Reserved ports" }), reservedBox()),
      el("div", { class: "card" }, el("h2", { text: "Recent activity" }), activityBox()));
    loadTokens();
  }

  // ------------------------------------------------------------ admin

  function adminNav(active) {
    setNav([["Computers", "#/devices"], ["Account", "#/account"], ["Admin", "#/admin", active === "overview"],
      ["Users", "#/admin/users", active === "users"], ["Ports", "#/admin/ports", active === "ports"],
      ["Audit", "#/admin/audit", active === "audit"]]);
  }

  async function adminPage() {
    if (!requireLogin()) return;
    if (me.role !== "admin") {
      setView(empty("Administrators only", "Your account does not have admin access.", null));
      return;
    }
    adminNav("overview");
    const box = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    setView(el("h1", { text: "Overview" }), box);
    const o = await act(() => api("GET", "/admin/overview"));
    if (o === undefined) return;
    box.replaceChildren(
      el("div", { class: "grid stats" },
        statCard(o.users, "Users"),
        statCard(`${o.devices_online} / ${o.devices}`, "Computers online"),
        statCard(o.tunnels, "Tunnels"),
        statCard(`${o.tunnels} / ${o.ports_in_pool}`, "Ports in use"),
        statCard(fmtBytes(o.today_bytes_in + o.today_bytes_out), "Traffic today"),
        statCard(fmtBytes(o.last30_bytes_in + o.last30_bytes_out), "Traffic, 30 days")),
      el("h2", { text: "Busiest tunnels (30 days)" }),
      o.top_tunnels_30d.length === 0
        ? el("p", { class: "muted", text: "No traffic yet." })
        : table(["Tunnel", "Owner", { label: "Total", num: true }],
          o.top_tunnels_30d.map(t => el("tr", null,
            el("td", { text: t.name }), el("td", { class: "muted", text: t.owner }),
            el("td", { class: "num", text: fmtBytes(t.bytes_in + t.bytes_out) })))));
  }

  async function adminUsersPage() {
    if (!requireLogin()) return;
    if (me.role !== "admin") return adminPage();
    adminNav("users");
    const box = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    const email = input({ type: "email", required: true, maxlength: 254 });
    const pw = input({ type: "password", required: true, minlength: 10, maxlength: 256, autocomplete: "new-password" });
    const role = el("select", null, el("option", { value: "user", text: "User" }), el("option", { value: "admin", text: "Admin" }));
    const msg = el("p", { class: "error", role: "status" });

    const load = async () => {
      const us = await act(() => api("GET", "/admin/users"));
      if (us === undefined) { box.replaceChildren(errorNote("Could not load users.")); return; }
      box.replaceChildren(table(["Email", "Role", "Computers limit", "Tunnels limit", "Status", ""],
        us.map(u => el("tr", null,
          el("td", { text: u.email }),
          el("td", { text: u.role }),
          el("td", null, quotaInput(u, "max_devices", load)),
          el("td", null, quotaInput(u, "max_tunnels_per_device", load)),
          el("td", null, u.disabled ? badge("disabled", "bad") : badge("active", "good")),
          el("td", { class: "actions-cell" },
            button(u.disabled ? "Enable" : "Disable", async () => {
              if (await act(() => api("PATCH", "/admin/users/" + u.id, { disabled: !u.disabled }),
                u.disabled ? "User enabled" : "User disabled")) load();
            }, { small: true }),
            button(u.role === "admin" ? "Remove admin" : "Make admin", async () => {
              if (await act(() => api("PATCH", "/admin/users/" + u.id, { role: u.role === "admin" ? "user" : "admin" }),
                "Role updated")) load();
            }, { small: true }))))));
    };

    setView(el("h1", { text: "Users" }),
      el("div", { class: "card" },
        el("h2", { text: "Create a user" }),
        el("form", { class: "row wrap", onsubmit: async (ev) => {
          ev.preventDefault();
          msg.textContent = "";
          const u = await act(() => api("POST", "/admin/users", { email: email.value, password: pw.value, role: role.value }),
            "User created");
          if (u) { email.value = ""; pw.value = ""; load(); }
          else msg.textContent = "Could not create the user. The email may already be registered.";
        } },
          field("Email", email),
          field("Starting password", pw, "They can change it later."),
          field("Role", role),
          el("button", { type: "submit", class: "primary", text: "Create user" })),
        msg),
      box);
    load();
  }

  function quotaInput(u, key, reload) {
    const cur = u[key];
    const i = input({ type: "number", min: -1, max: 1000, value: cur === null ? "" : cur, placeholder: "default",
      class: "quota", "aria-label": key.replace(/_/g, " ") });
    return el("form", { class: "row nowrap", onsubmit: async (ev) => {
      ev.preventDefault();
      const v = i.value === "" ? -1 : Number(i.value);
      if (await act(() => api("PATCH", "/admin/users/" + u.id, { [key]: v }), "Limit saved")) reload();
    } }, i, el("button", { type: "submit", class: "small", text: "Save" }));
  }

  async function adminPortsPage() {
    if (!requireLogin()) return;
    if (me.role !== "admin") return adminPage();
    adminNav("ports");
    const box = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    const type = el("select", null, el("option", { value: "tcp", text: "TCP" }), el("option", { value: "udp", text: "UDP" }));
    const port = input({ type: "number", min: 1, max: 65535, required: true, class: "quota" });
    const uid = input({ type: "number", min: 1, placeholder: "user ID (blank = admins only)" });
    const note = input({ maxlength: 200, placeholder: "e.g. family Minecraft server" });
    const msg = el("p", { class: "error", role: "status" });

    const load = async () => {
      const rs = await act(() => api("GET", "/admin/reserved-ports"));
      if (rs === undefined) { box.replaceChildren(errorNote("Could not load reserved ports.")); return; }
      box.replaceChildren(rs.length === 0
        ? el("p", { class: "muted", text: "No reserved ports." })
        : table(["Port", "For", "In use by", "Note", ""], rs.map(r => el("tr", null,
          el("td", null, el("code", { text: `${r.remote_port}/${r.type}` })),
          el("td", { text: r.user_email || "admins only" }),
          el("td", { class: "muted", text: r.in_use_by || "–" }),
          el("td", { class: "muted", text: r.note }),
          el("td", null, button("Remove", async () => {
            const ok = await confirmBox("Remove reservation?",
              `Port ${r.remote_port}/${r.type} becomes available again.`, "Remove", true);
            if (ok && await act(() => api("DELETE", `/admin/reserved-ports/${r.type}/${r.remote_port}`),
              "Reservation removed")) load();
          }, { small: true, danger: true }))))));
    };

    setView(el("h1", { text: "Reserved ports" }),
      el("div", { class: "card" },
        el("p", { class: "muted", text: "Reserved ports are never given out automatically. Use them for fixed server ports, such as 25565 for Minecraft. Ports outside the public pool must also be allowed on the relay." }),
        el("form", { class: "row wrap", onsubmit: async (ev) => {
          ev.preventDefault();
          msg.textContent = "";
          const body = { note: note.value, user_id: uid.value === "" ? null : Number(uid.value) };
          if (await act(() => api("PUT", `/admin/reserved-ports/${type.value}/${port.value}`, body), "Port reserved")) load();
          else msg.textContent = "Could not reserve that port. It may already be in use by someone else.";
        } },
          field("Protocol", type), field("Port", port), field("For user ID", uid), field("Note", note),
          el("button", { type: "submit", class: "primary", text: "Reserve" })),
        msg),
      box);
    load();
  }

  async function adminAuditPage() {
    if (!requireLogin()) return;
    if (me.role !== "admin") return adminPage();
    adminNav("audit");
    const box = el("div", null, el("p", { class: "muted", text: "Loading…" }));
    setView(el("h1", { text: "Audit log" }), box);
    const es = await act(() => api("GET", "/admin/audit"));
    if (es === undefined) { box.replaceChildren(errorNote("Could not load the audit log.")); return; }
    box.replaceChildren(table(["When", "Who", "Action", "Target", "IP"], es.map(e => el("tr", null,
      el("td", null, timeEl(e.at)),
      el("td", { text: e.actor_user_id ? "user " + e.actor_user_id : (e.actor_device_id || "system") }),
      el("td", { text: e.action }),
      el("td", { class: "muted", text: e.target }),
      el("td", { class: "muted", text: e.ip })))));
  }

  function notFound() {
    setNav([]);
    setView(empty("Page not found", "The link may be old or mistyped.",
      el("a", { href: "#/", text: "Go to the start page" })));
  }

  // ------------------------------------------------------------ router

  async function route() {
    stopTimers();
    if (!me) await loadSession();
    const raw = location.hash.replace(/^#/, "") || "/";
    const [path, query] = raw.split("?");
    const params = new URLSearchParams(query || "");
    const parts = path.split("/").filter(Boolean).map(decodeURIComponent);

    if (parts.length === 0) {
      location.hash = me ? "#/devices" : "#/login";
      return;
    }
    switch (parts[0]) {
      case "login": return loginPage(false);
      case "register":
        if (info && info.open_registration) return loginPage(true);
        return notFound();
      case "claim": return claimPage(params);
      case "devices":
        if (parts.length === 1) return devicesPage();
        if (parts[2] === "tunnels" && parts[3]) return tunnelPage(parts[1], parts[3]);
        return devicePage(parts[1]);
      case "account": return accountPage();
      case "admin":
        if (parts[1] === "users") return adminUsersPage();
        if (parts[1] === "ports") return adminPortsPage();
        if (parts[1] === "audit") return adminAuditPage();
        return adminPage();
      default: return notFound();
    }
  }

  window.addEventListener("hashchange", route);
  loadSession().then(route);
})();
