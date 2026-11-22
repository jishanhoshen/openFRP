// TunnelHub dashboard. Built with DOM APIs only (no innerHTML), so device
// names and other user-supplied text are never interpreted as markup.
(() => {
  "use strict";

  const app = document.getElementById("app");
  const nav = document.getElementById("nav");
  const who = document.getElementById("who");
  let me = null;
  let info = null;

  // ---------------------------------------------------------------- helpers

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
    app.replaceChildren(...nodes.flat());
  }

  function errorBox(msg) {
    return el("p", { class: "error", role: "alert", text: msg });
  }

  async function api(method, path, body) {
    const opt = { method, credentials: "same-origin", headers: {} };
    if (body !== undefined) {
      opt.headers["Content-Type"] = "application/json";
      opt.body = JSON.stringify(body);
    }
    const resp = await fetch("/api/v1" + path, opt);
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

  function fmtBytes(n) {
    if (n === null || n === undefined) return "–";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0, v = Number(n);
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(v < 10 ? 2 : 1)) + " " + units[i];
  }

  function fmtTime(s) {
    if (!s) return "never";
    const d = new Date(s);
    return isNaN(d) ? s : d.toLocaleString();
  }

  function pill(text, kind) {
    return el("span", { class: "pill " + (kind || ""), text });
  }

  function button(label, onclick, opts = {}) {
    return el("button", { type: "button", class: [opts.primary ? "primary" : "", opts.danger ? "danger" : ""].join(" "),
      onclick, disabled: opts.disabled, text: label });
  }

  function field(label, input) {
    return el("label", null, label, input);
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

  function setNav(items) {
    nav.replaceChildren(...items.map(([label, href, active]) =>
      el("a", { href, class: active ? "active" : "", text: label })));
  }

  // ---------------------------------------------------------------- session

  async function loadSession() {
    try {
      me = await api("GET", "/me");
    } catch (e) {
      me = null;
    }
    try { info = await api("GET", "/info"); } catch (_) { info = {}; }
    who.replaceChildren();
    if (me) {
      who.append(me.email + (me.role === "admin" ? " (admin) " : " "),
        el("button", { type: "button", onclick: logout, text: "Log out" }));
    }
  }

  async function logout() {
    try { await api("POST", "/auth/logout"); } catch (_) { /* ignore */ }
    me = null;
    location.hash = "#/login";
    route();
  }

  // ---------------------------------------------------------------- pages

  function loginPage(register) {
    const email = input({ type: "email", name: "email", required: true, autocomplete: "username", maxlength: 254 });
    const pw = input({ type: "password", name: "password", required: true,
      autocomplete: register ? "new-password" : "current-password", minlength: 10, maxlength: 256 });
    const msg = el("p", { class: "error", role: "alert" });
    const form = el("form", { class: "card", onsubmit: async (ev) => {
      ev.preventDefault();
      msg.textContent = "";
      try {
        if (register) await api("POST", "/auth/register", { email: email.value, password: pw.value });
        await api("POST", "/auth/login", { email: email.value, password: pw.value });
        await loadSession();
        location.hash = "#/devices";
        route();
      } catch (e) {
        msg.textContent = e.message;
      }
    } },
      el("h1", { text: register ? "Create account" : "Log in" }),
      field("Email", email), el("p"), field("Password", pw), el("p"),
      el("button", { type: "submit", class: "primary", text: register ? "Register" : "Log in" }),
      msg,
      el("p", { class: "muted" }, register
        ? [el("a", { href: "#/login", text: "Have an account?" })]
        : (info && info.open_registration ? [el("a", { href: "#/register", text: "Create an account" })] : [])));
    setNav([]);
    setView(form);
  }

  function requireLogin() {
    if (!me) {
      location.hash = "#/login";
      return false;
    }
    return true;
  }

  async function devicesPage() {
    if (!requireLogin()) return;
    setNav([["Devices", "#/devices", true], ["Account", "#/account"],
      ...(me.role === "admin" ? [["Admin", "#/admin"]] : [])]);
    const list = el("div", null, "Loading…");
    setView(
      el("h1", { text: "Your devices" }),
      el("div", { class: "card" },
        el("h2", { style: "margin-top:0", text: "Link a new computer" }),
        el("p", { class: "muted", text: "Run the agent on the computer, then enter the code it shows." }),
        claimForm(), ),
      list);
    try {
      const ds = await api("GET", "/devices");
      list.replaceChildren(ds.length === 0
        ? el("p", { class: "muted", text: "No devices yet." })
        : table(["Name", "Status", "Last seen", { label: "Agent", num: false }],
          ds.map(d => el("tr", null,
            el("td", null, el("a", { href: "#/devices/" + d.id, text: d.name })),
            el("td", null, d.online ? pill("online", "on") : pill("offline", "off")),
            el("td", null, fmtTime(d.last_seen_at)),
            el("td", { class: "muted" }, [d.os, d.arch, d.agent_version].filter(Boolean).join(" "))))));
    } catch (e) {
      list.replaceChildren(errorBox(e.message));
    }
  }

  function claimForm(initialCode) {
    const code = input({ name: "code", required: true, placeholder: "ABCD-EFGH", value: initialCode || "",
      autocomplete: "off", maxlength: 16, style: "width:12em;text-transform:uppercase" });
    const name = input({ name: "name", placeholder: "Name (optional)", maxlength: 64 });
    const msg = el("p", { class: "error", role: "status" });
    return el("form", { class: "row", onsubmit: async (ev) => {
      ev.preventDefault();
      msg.textContent = "";
      try {
        const d = await api("POST", "/devices/claim", { code: code.value, name: name.value });
        location.hash = "#/devices/" + d.id;
      } catch (e) {
        msg.textContent = e.message;
      }
    } }, field("Pairing code", code), field("Name", name),
      el("button", { type: "submit", class: "primary", text: "Link device" }), msg);
  }

  async function claimPage(params) {
    if (!requireLogin()) return;
    setNav([["Devices", "#/devices"], ["Account", "#/account"]]);
    setView(el("div", { class: "card" }, el("h1", { text: "Link a computer" }),
      el("p", { class: "muted", text: "Confirm the code shown by the agent to link this computer to your account." }),
      claimForm(params.get("code") || "")));
  }

  async function devicePage(id) {
    if (!requireLogin()) return;
    setNav([["Devices", "#/devices"], ["Account", "#/account"],
      ...(me.role === "admin" ? [["Admin", "#/admin"]] : [])]);
    const body = el("div", null, "Loading…");
    setView(body);
    let d;
    try {
      d = await api("GET", "/devices/" + encodeURIComponent(id));
    } catch (e) {
      body.replaceChildren(errorBox(e.message));
      return;
    }
    const renameInput = input({ value: d.name, maxlength: 64, "aria-label": "Device name" });
    const tunnelsBox = el("div", null, "Loading…");
    body.replaceChildren(
      el("h1", null, d.name, " ", d.online ? pill("online", "on") : pill("offline", "off")),
      el("div", { class: "card" },
        el("p", null, "Last seen ", el("b", { text: fmtTime(d.last_seen_at) }), " from ", el("code", { text: d.last_ip || "–" })),
        el("p", { class: "muted" }, [d.hostname, d.os, d.arch, d.agent_version].filter(Boolean).join(" · ")),
        el("form", { class: "row", onsubmit: async (ev) => {
          ev.preventDefault();
          try { await api("PATCH", "/devices/" + id, { name: renameInput.value }); devicePage(id); }
          catch (e) { alert(e.message); }
        } }, field("Name", renameInput), el("button", { type: "submit", text: "Rename" })),
        el("p", null,
          button(d.disabled ? "Resume tunnels" : "Pause tunnels", async () => {
            try { await api("PATCH", "/devices/" + id, { disabled: !d.disabled }); devicePage(id); }
            catch (e) { alert(e.message); }
          }),
          " ",
          button("Remove device", async () => {
            if (!confirm("Remove this device? Its tunnels are released and the agent is disconnected for good.")) return;
            try { await api("DELETE", "/devices/" + id); location.hash = "#/devices"; route(); }
            catch (e) { alert(e.message); }
          }, { danger: true }))),
      el("h2", { text: "Tunnels" }),
      tunnelsBox,
      tunnelForm(id, () => devicePage(id)));
    loadTunnels(id, tunnelsBox);
  }

  async function loadTunnels(deviceID, box) {
    try {
      const ts = await api("GET", `/devices/${deviceID}/tunnels`);
      if (ts.length === 0) {
        box.replaceChildren(el("p", { class: "muted", text: "No tunnels yet. Add one below." }));
        return;
      }
      box.replaceChildren(table(["Name", "Public address", "Local", "Status", "Today", { label: "", num: false }],
        ts.map(t => el("tr", null,
          el("td", null, el("a", { href: `#/devices/${deviceID}/tunnels/${t.id}`, text: t.name }),
            el("div", { class: "muted", text: t.type.toUpperCase() + (t.proxy_protocol ? " · PROXY " + t.proxy_protocol : "") })),
          el("td", null, el("code", { text: t.public_address })),
          el("td", { class: "muted", text: `${t.local_ip}:${t.local_port}` }),
          el("td", null, !t.enabled ? pill("disabled", "off") : t.live && t.live.online ? pill("live", "on") : pill("waiting", "off")),
          el("td", { class: "num", text: t.live ? fmtBytes(t.live.today_bytes_in + t.live.today_bytes_out) : "–" }),
          el("td", null, button(t.enabled ? "Disable" : "Enable", async () => {
            try { await api("PATCH", `/devices/${deviceID}/tunnels/${t.id}`, { enabled: !t.enabled }); loadTunnels(deviceID, box); }
            catch (e) { alert(e.message); }
          }), " ", button("Delete", async () => {
            if (!confirm(`Delete tunnel ${t.name}? Its port goes into a cooldown before anyone else can get it.`)) return;
            try { await api("DELETE", `/devices/${deviceID}/tunnels/${t.id}`); loadTunnels(deviceID, box); }
            catch (e) { alert(e.message); }
          }, { danger: true }))))));
    } catch (e) {
      box.replaceChildren(errorBox(e.message));
    }
  }

  function tunnelForm(deviceID, done) {
    const name = input({ name: "name", required: true, pattern: "[a-z0-9][a-z0-9-]{0,47}", maxlength: 48, placeholder: "minecraft" });
    const type = el("select", { name: "type" }, el("option", { value: "tcp", text: "TCP" }), el("option", { value: "udp", text: "UDP" }));
    const lport = input({ name: "local_port", type: "number", min: 1, max: 65535, required: true, style: "width:8em", placeholder: "25565" });
    const lip = input({ name: "local_ip", value: "127.0.0.1", maxlength: 45, style: "width:10em" });
    const proxy = el("select", { name: "proxy_protocol" },
      el("option", { value: "", text: "No PROXY header" }), el("option", { value: "v2", text: "PROXY v2" }),
      el("option", { value: "v1", text: "PROXY v1" }));
    const msg = el("p", { class: "error", role: "status" });
    return el("div", { class: "card" }, el("h2", { style: "margin-top:0", text: "Add a tunnel" }),
      el("form", { class: "row", onsubmit: async (ev) => {
        ev.preventDefault();
        msg.textContent = "";
        const body = { name: name.value, type: type.value, local_port: Number(lport.value), local_ip: lip.value,
          remote_port: 0 };
        if (type.value === "tcp" && proxy.value) body.proxy_protocol = proxy.value;
        try { await api("POST", `/devices/${deviceID}/tunnels`, body); done(); }
        catch (e) { msg.textContent = e.message; }
      } },
      field("Name", name), field("Protocol", type), field("Local port", lport), field("Local address", lip),
      field("Send client IP (TCP)", proxy),
      el("button", { type: "submit", class: "primary", text: "Create" })),
      el("p", { class: "muted", text: "The public port is assigned automatically." }), msg);
  }

  async function tunnelPage(deviceID, tid) {
    if (!requireLogin()) return;
    setNav([["Devices", "#/devices"], ["Account", "#/account"]]);
    const body = el("div", null, "Loading…");
    setView(body);
    try {
      const [t, days] = await Promise.all([
        api("GET", `/devices/${deviceID}/tunnels/${tid}`),
        api("GET", `/devices/${deviceID}/tunnels/${tid}/traffic?days=30`),
      ]);
      const total = days.days.reduce((a, d) => ({ i: a.i + d.bytes_in, o: a.o + d.bytes_out }), { i: 0, o: 0 });
      body.replaceChildren(
        el("p", null, el("a", { href: "#/devices/" + deviceID, text: "← Back to device" })),
        el("h1", { text: t.name }),
        el("div", { class: "grid" },
          el("div", { class: "stat" }, el("div", { class: "v", text: t.public_address }), el("div", { class: "l", text: "Share this address" })),
          el("div", { class: "stat" }, el("div", { class: "v", text: fmtBytes(total.i) }), el("div", { class: "l", text: "Received, 30 days" })),
          el("div", { class: "stat" }, el("div", { class: "v", text: fmtBytes(total.o) }), el("div", { class: "l", text: "Sent, 30 days" })),
          el("div", { class: "stat" }, el("div", { class: "v", text: t.live && t.live.online ? "Live" : "Idle" }),
            el("div", { class: "l", text: `${t.live ? t.live.connections : 0} open connections` }))),
        el("h2", { text: "Daily traffic" }),
        days.days.length === 0
          ? el("p", { class: "muted", text: "No traffic recorded yet." })
          : table(["Day (UTC)", { label: "In", num: true }, { label: "Out", num: true }],
            days.days.slice().reverse().map(d => el("tr", null,
              el("td", { text: d.day }), el("td", { class: "num", text: fmtBytes(d.bytes_in) }),
              el("td", { class: "num", text: fmtBytes(d.bytes_out) })))));
    } catch (e) {
      body.replaceChildren(errorBox(e.message));
    }
  }

  async function accountPage() {
    if (!requireLogin()) return;
    setNav([["Devices", "#/devices"], ["Account", "#/account", true],
      ...(me.role === "admin" ? [["Admin", "#/admin"]] : [])]);
    const tokenBox = el("div");
    const tokensBox = el("div", null, "Loading…");
    const pwMsg = el("p", { class: "error", role: "status" });
    const cur = input({ type: "password", autocomplete: "current-password", required: true });
    const next = input({ type: "password", autocomplete: "new-password", minlength: 10, required: true });
    const loadTokens = async () => {
      try {
        const ts = await api("GET", "/tokens");
        tokensBox.replaceChildren(ts.length === 0 ? el("p", { class: "muted", text: "No API tokens." })
          : table(["Name", "Prefix", "Created", ""], ts.map(t => el("tr", null,
            el("td", { text: t.name }), el("td", null, el("code", { text: t.prefix + "…" })),
            el("td", { text: fmtTime(t.created_at) }),
            el("td", null, button("Revoke", async () => {
              try { await api("DELETE", "/tokens/" + t.id); loadTokens(); } catch (e) { alert(e.message); }
            }, { danger: true }))))));
      } catch (e) { tokensBox.replaceChildren(errorBox(e.message)); }
    };
    const nameIn = input({ maxlength: 64, required: true, placeholder: "laptop script" });
    setView(
      el("h1", { text: "Account" }),
      el("div", { class: "card" }, el("p", null, "Signed in as ", el("b", { text: me.email })),
        el("form", { class: "row", onsubmit: async (ev) => {
          ev.preventDefault();
          pwMsg.textContent = "";
          try {
            await api("POST", "/me/password", { current_password: cur.value, new_password: next.value });
            pwMsg.className = "ok"; pwMsg.textContent = "Password changed. Other sessions were logged out.";
            cur.value = ""; next.value = "";
          } catch (e) { pwMsg.className = "error"; pwMsg.textContent = e.message; }
        } }, field("Current password", cur), field("New password", next),
          el("button", { type: "submit", text: "Change password" })), pwMsg),
      el("div", { class: "card" }, el("h2", { style: "margin-top:0", text: "API tokens" }),
        el("p", { class: "muted", text: "Tokens let scripts use the API as you (Authorization: Bearer thk_…). Shown once." }),
        el("form", { class: "row", onsubmit: async (ev) => {
          ev.preventDefault();
          try {
            const t = await api("POST", "/tokens", { name: nameIn.value });
            tokenBox.replaceChildren(el("p", null, "New token (copy now, it will not be shown again): ",
              el("code", { text: t.token })));
            nameIn.value = ""; loadTokens();
          } catch (e) { alert(e.message); }
        } }, field("Name", nameIn), el("button", { type: "submit", text: "Create token" })),
        tokenBox, tokensBox),
      el("div", { class: "card" }, el("h2", { style: "margin-top:0", text: "Reserved ports" }), placeholder(reservedBox)),
      el("div", { class: "card" }, el("h2", { style: "margin-top:0", text: "Your recent activity" }), placeholder(activityBox)));
    loadTokens();
  }

  // Renders an async builder into a node that is filled in when it resolves.
  function placeholder(build) {
    const holder = el("div", null, "Loading…");
    build().then(node => holder.replaceChildren(node));
    return holder;
  }

  async function reservedBox() {
    const box = el("div", null, "Loading…");
    try {
      const rs = await api("GET", "/reserved-ports");
      if (rs.length === 0) {
        box.replaceChildren(el("p", { class: "muted", text: "You have no reserved ports. Ask an administrator for one." }));
      } else {
        const rows = rs.map(r => el("tr", null,
          el("td", null, el("code", { text: String(r.remote_port) })),
          el("td", { text: r.type.toUpperCase() }),
          el("td", { class: "muted", text: r.note })));
        box.replaceChildren(table(["Port", "Protocol", "Note"], rows));
      }
    } catch (e) {
      box.replaceChildren(errorBox(e.message));
    }
    return box;
  }

  async function activityBox() {
    const box = el("div", null, "Loading…");
    try {
      const es = await api("GET", "/audit");
      const rows = es.slice(0, 30).map(e => el("tr", null,
        el("td", { text: fmtTime(e.at) }),
        el("td", { text: e.action }),
        el("td", { class: "muted", text: e.target })));
      box.replaceChildren(table(["When", "Action", "Target"], rows));
    } catch (e) {
      box.replaceChildren(errorBox(e.message));
    }
    return box;
  }

  async function adminPage() {
    if (!requireLogin()) return;
    if (me.role !== "admin") { setView(errorBox("Administrators only.")); return; }
    setNav([["Devices", "#/devices"], ["Account", "#/account"], ["Admin", "#/admin", true],
      ["Users", "#/admin/users"], ["Ports", "#/admin/ports"], ["Audit", "#/admin/audit"]]);
    const box = el("div", null, "Loading…");
    setView(el("h1", { text: "Overview" }), box);
    try {
      const o = await api("GET", "/admin/overview");
      box.replaceChildren(
        el("div", { class: "grid" },
          stat(o.users, "Users"), stat(`${o.devices_online} / ${o.devices}`, "Devices online"),
          stat(o.tunnels, "Tunnels"), stat(`${o.tunnels} / ${o.ports_in_pool}`, "Ports in use"),
          stat(fmtBytes(o.today_bytes_in + o.today_bytes_out), "Traffic today"),
          stat(fmtBytes(o.last30_bytes_in + o.last30_bytes_out), "Traffic, 30 days")),
        el("h2", { text: "Busiest tunnels (30 days)" }),
        o.top_tunnels_30d.length === 0 ? el("p", { class: "muted", text: "No traffic yet." })
          : table(["Tunnel", "Owner", { label: "Total", num: true }],
            o.top_tunnels_30d.map(t => el("tr", null, el("td", { text: t.name }), el("td", { class: "muted", text: t.owner }),
              el("td", { class: "num", text: fmtBytes(t.bytes_in + t.bytes_out) })))));
    } catch (e) { box.replaceChildren(errorBox(e.message)); }
  }

  function stat(v, l) {
    return el("div", { class: "stat" }, el("div", { class: "v", text: String(v) }), el("div", { class: "l", text: l }));
  }

  async function adminUsersPage() {
    if (!requireLogin() || me.role !== "admin") return adminPage();
    setNav([["Devices", "#/devices"], ["Account", "#/account"], ["Admin", "#/admin"],
      ["Users", "#/admin/users", true], ["Ports", "#/admin/ports"], ["Audit", "#/admin/audit"]]);
    const box = el("div", null, "Loading…");
    const email = input({ type: "email", required: true, maxlength: 254 });
    const pw = input({ type: "password", required: true, minlength: 10, maxlength: 256, autocomplete: "new-password" });
    const role = el("select", null, el("option", { value: "user", text: "user" }), el("option", { value: "admin", text: "admin" }));
    const msg = el("p", { class: "error", role: "status" });
    const load = async () => {
      try {
        const us = await api("GET", "/admin/users");
        box.replaceChildren(table(["Email", "Role", "Devices limit", "Tunnels limit", "State", ""],
          us.map(u => el("tr", null,
            el("td", { text: u.email }), el("td", { text: u.role }),
            el("td", null, quotaInput(u, "max_devices", load)),
            el("td", null, quotaInput(u, "max_tunnels_per_device", load)),
            el("td", null, u.disabled ? pill("disabled", "bad") : pill("active", "on")),
            el("td", null, button(u.disabled ? "Enable" : "Disable", async () => {
              try { await api("PATCH", "/admin/users/" + u.id, { disabled: !u.disabled }); load(); }
              catch (e) { alert(e.message); }
            }), " ", button(u.role === "admin" ? "Demote" : "Make admin", async () => {
              try { await api("PATCH", "/admin/users/" + u.id, { role: u.role === "admin" ? "user" : "admin" }); load(); }
              catch (e) { alert(e.message); }
            }))))));
      } catch (e) { box.replaceChildren(errorBox(e.message)); }
    };
    setView(el("h1", { text: "Users" }),
      el("div", { class: "card" }, el("h2", { style: "margin-top:0", text: "Create user" }),
        el("form", { class: "row", onsubmit: async (ev) => {
          ev.preventDefault(); msg.textContent = "";
          try {
            await api("POST", "/admin/users", { email: email.value, password: pw.value, role: role.value });
            email.value = ""; pw.value = ""; msg.className = "ok"; msg.textContent = "User created."; load();
          } catch (e) { msg.className = "error"; msg.textContent = e.message; }
        } }, field("Email", email), field("Initial password", pw), field("Role", role),
          el("button", { type: "submit", class: "primary", text: "Create" })), msg),
      box);
    load();
  }

  function quotaInput(u, key, reload) {
    const cur = u[key];
    const i = input({ type: "number", min: -1, max: 1000, value: cur === null ? "" : cur, placeholder: "default",
      style: "width:6em", "aria-label": key });
    return el("form", { class: "row", style: "flex-wrap:nowrap", onsubmit: async (ev) => {
      ev.preventDefault();
      const v = i.value === "" ? -1 : Number(i.value);
      try { await api("PATCH", "/admin/users/" + u.id, { [key]: v }); reload(); }
      catch (e) { alert(e.message); }
    } }, i, el("button", { type: "submit", text: "Set" }));
  }

  async function adminPortsPage() {
    if (!requireLogin() || me.role !== "admin") return adminPage();
    setNav([["Devices", "#/devices"], ["Account", "#/account"], ["Admin", "#/admin"],
      ["Users", "#/admin/users"], ["Ports", "#/admin/ports", true], ["Audit", "#/admin/audit"]]);
    const box = el("div", null, "Loading…");
    const type = el("select", null, el("option", { value: "tcp", text: "tcp" }), el("option", { value: "udp", text: "udp" }));
    const port = input({ type: "number", min: 1, max: 65535, required: true, style: "width:8em" });
    const uid = input({ type: "number", min: 1, placeholder: "user id (blank = admins only)", style: "width:14em" });
    const note = input({ maxlength: 200, placeholder: "note" });
    const msg = el("p", { class: "error", role: "status" });
    const load = async () => {
      try {
        const rs = await api("GET", "/admin/reserved-ports");
        box.replaceChildren(rs.length === 0 ? el("p", { class: "muted", text: "No reservations." })
          : table(["Port", "For", "In use", "Note", ""], rs.map(r => el("tr", null,
            el("td", null, el("code", { text: `${r.remote_port}/${r.type}` })),
            el("td", { text: r.user_email || "admins only" }),
            el("td", { class: "muted", text: r.in_use_by || "–" }), el("td", { class: "muted", text: r.note }),
            el("td", null, button("Remove", async () => {
              try { await api("DELETE", `/admin/reserved-ports/${r.type}/${r.remote_port}`); load(); }
              catch (e) { alert(e.message); }
            }, { danger: true }))))));
      } catch (e) { box.replaceChildren(errorBox(e.message)); }
    };
    setView(el("h1", { text: "Reserved ports" }),
      el("div", { class: "card" },
        el("p", { class: "muted", text: "Reserved ports are never handed out automatically. Use them for well-known server ports (e.g. 25565/tcp for Minecraft). They must also be allowed in the relay's frps configuration when outside the pool." }),
        el("form", { class: "row", onsubmit: async (ev) => {
          ev.preventDefault(); msg.textContent = "";
          const body = { note: note.value, user_id: uid.value === "" ? null : Number(uid.value) };
          try {
            await api("PUT", `/admin/reserved-ports/${type.value}/${port.value}`, body);
            msg.className = "ok"; msg.textContent = "Saved."; load();
          } catch (e) { msg.className = "error"; msg.textContent = e.message; }
        } }, field("Protocol", type), field("Port", port), field("User", uid), field("Note", note),
          el("button", { type: "submit", class: "primary", text: "Reserve" })), msg),
      box);
    load();
  }

  async function adminAuditPage() {
    if (!requireLogin() || me.role !== "admin") return adminPage();
    setNav([["Devices", "#/devices"], ["Account", "#/account"], ["Admin", "#/admin"],
      ["Users", "#/admin/users"], ["Ports", "#/admin/ports"], ["Audit", "#/admin/audit", true]]);
    const box = el("div", null, "Loading…");
    setView(el("h1", { text: "Audit log" }), box);
    try {
      const es = await api("GET", "/admin/audit");
      box.replaceChildren(table(["When", "Actor", "Action", "Target", "IP"], es.map(e => el("tr", null,
        el("td", { text: fmtTime(e.at) }), el("td", { text: e.actor_user_id ? "user " + e.actor_user_id : (e.actor_device_id || "system") }),
        el("td", { text: e.action }), el("td", { class: "muted", text: e.target }), el("td", { class: "muted", text: e.ip })))));
    } catch (e) { box.replaceChildren(errorBox(e.message)); }
  }

  function notFound() {
    setNav([]);
    setView(el("p", null, "Page not found. ", el("a", { href: "#/", text: "Go home" })));
  }

  // ---------------------------------------------------------------- router

  async function route() {
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
