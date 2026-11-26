// Remote Support Manager console. Plain JS, no inline handlers (CSP), and
// every value from the server is inserted with textContent.
"use strict";
(function () {
  const $ = (id) => document.getElementById(id);
  let csrf = "";
  let me = null;
  let tree = { clients: [], perms: {} };
  let selected = null;
  let mfaToken = "";
  let poll = null;
  let pin = "";

  function el(tag, props, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (k === "class") e.className = v;
      else if (k === "text") e.textContent = v;
      else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
      else if (v === true) e.setAttribute(k, "");
      else if (v !== false && v != null) e.setAttribute(k, v);
    }
    for (const kid of kids) if (kid != null) e.append(kid);
    return e;
  }

  async function api(method, path, body) {
    const opt = { method, headers: {}, credentials: "same-origin" };
    if (body !== undefined) { opt.headers["Content-Type"] = "application/json"; opt.body = JSON.stringify(body); }
    if (method !== "GET") opt.headers["X-CSRF-Token"] = csrf;
    const r = await fetch(path, opt);
    let data = {};
    try { data = await r.json(); } catch (_) { /* empty */ }
    if (!r.ok) { const err = new Error(data.error || ("Request failed (" + r.status + ")")); err.status = r.status; throw err; }
    return data;
  }

  function show(section) {
    for (const id of ["login", "devices", "admin", "audit"]) $(id).hidden = id !== section;
    for (const b of document.querySelectorAll(".tab")) b.classList.toggle("active", b.dataset.tab === section);
  }

  function fmtBytes(n) {
    if (!n) return "0 B";
    const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(i ? 1 : 0) + " " + u[i];
  }
  function fmtUptime(s) {
    const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    return (d ? d + "d " : "") + h + "h " + m + "m";
  }
  function fmtTime(t) { return t ? new Date(t * 1000).toLocaleString() : "never"; }
  function ago(t) {
    if (!t) return "never";
    const s = Math.max(0, Date.now() / 1000 - t);
    if (s < 90) return "just now";
    if (s < 3600) return Math.round(s / 60) + " min ago";
    if (s < 86400) return Math.round(s / 3600) + " h ago";
    return Math.round(s / 86400) + " days ago";
  }

  // ------------------------------------------------------------ sign-in
  $("login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    $("login-error").textContent = "";
    const f = new FormData(ev.target);
    try {
      const r = await api("POST", "/api/login", { email: f.get("email"), password: f.get("password") });
      mfaToken = r.mfaToken;
      $("login-form").hidden = true;
      $("mfa-form").hidden = false;
      $("mfa-enroll").hidden = !r.enroll;
      if (r.enroll) {
        $("mfa-qr").src = "/api/mfa/qr?token=" + encodeURIComponent(mfaToken);
        $("mfa-secret").textContent = r.secret.replace(/(.{4})/g, "$1 ").trim();
      }
      $("mfa-form").elements.code.focus();
    } catch (e) { $("login-error").textContent = e.message; }
  });

  $("mfa-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    $("mfa-error").textContent = "";
    try {
      const r = await api("POST", "/api/mfa/verify", { mfaToken, code: ev.target.elements.code.value });
      csrf = r.csrf;
      ev.target.reset();
      await start();
    } catch (e) {
      $("mfa-error").textContent = e.message;
      if (e.status === 401 && /start again/.test(e.message)) { $("mfa-form").hidden = true; $("login-form").hidden = false; }
    }
  });

  $("logout").addEventListener("click", async () => {
    try { await api("POST", "/api/logout"); } catch (_) { /* ignore */ }
    location.reload();
  });

  for (const b of document.querySelectorAll(".tab")) {
    b.addEventListener("click", () => {
      show(b.dataset.tab);
      if (b.dataset.tab === "audit") loadAudit();
      if (b.dataset.tab === "admin") loadAdmin();
    });
  }

  async function start() {
    try {
      const r = await api("GET", "/api/me");
      me = r.user; csrf = r.csrf; pin = r.pin || "";
      $("org").textContent = r.org;
      document.title = r.org + " · Remote Support Manager";
    } catch (_) {
      $("tabs").hidden = true; $("who").hidden = true;
      $("login-form").hidden = false; $("mfa-form").hidden = true;
      show("login");
      return;
    }
    $("tabs").hidden = false; $("who").hidden = false;
    $("who-name").textContent = me.name + (me.isAdmin ? " (administrator)" : "");
    for (const e of document.querySelectorAll(".admin-only")) e.hidden = !me.isAdmin;
    show("devices");
    await loadTree();
    setInterval(loadTree, 15000);
  }

  // ------------------------------------------------------------ inventory
  let searchTimer = null;
  $("search").addEventListener("input", () => { clearTimeout(searchTimer); searchTimer = setTimeout(loadTree, 200); });

  async function loadTree() {
    try {
      tree = await api("GET", "/api/tree?q=" + encodeURIComponent($("search").value));
    } catch (e) { if (e.status === 401) location.reload(); return; }
    renderTree();
    if (selected) {
      const d = findDevice(selected.id);
      if (d) { selected = d; renderDetailInfo(); }
    }
  }

  function findDevice(id) {
    for (const c of tree.clients) for (const s of c.sites) for (const d of s.devices) if (d.id === id) return d;
    return null;
  }

  function renderTree() {
    const root = $("tree");
    const open = new Set([...root.querySelectorAll("details[open]")].map((d) => d.dataset.key));
    const filtering = $("search").value.trim() !== "";
    root.replaceChildren();
    if (!tree.clients.length) { root.append(el("p", { class: "muted", text: filtering ? "No matches." : "No clients yet." })); return; }
    for (const c of tree.clients) {
      const online = c.sites.reduce((n, s) => n + s.devices.filter((d) => d.online).length, 0);
      const total = c.sites.reduce((n, s) => n + s.devices.length, 0);
      const cd = el("details", { class: "t-client", "data-key": "c" + c.id, open: filtering || open.has("c" + c.id) || tree.clients.length === 1 },
        el("summary", { role: "treeitem" }, c.name + " ", el("span", { class: "badge", text: online + "/" + total + " online" })));
      for (const s of c.sites) {
        const sd = el("details", { class: "t-site", "data-key": "s" + s.id, open: filtering || open.has("s" + s.id) || c.sites.length === 1 },
          el("summary", { role: "treeitem" }, s.name + " ", el("span", { class: "badge", text: String(s.devices.length) })));
        for (const d of s.devices) {
          sd.append(el("button", { type: "button", role: "treeitem", class: "t-dev" + (selected && selected.id === d.id ? " sel" : ""), onclick: () => selectDevice(d) },
            el("span", { class: "dot" + (d.online ? " on" : ""), title: d.online ? "Online" : "Offline" }),
            el("span", { text: d.hostname || d.id }),
            el("span", { class: "muted", text: d.online ? "" : "· " + ago(d.lastSeen) })));
        }
        cd.append(sd);
      }
      root.append(cd);
    }
  }

  function selectDevice(d) {
    selected = d;
    renderTree();
    renderDetail();
  }

  function renderDetailInfo() {
    const d = selected;
    const info = $("detail-info");
    if (!info) return;
    const disks = el("div");
    for (const k of d.disks || []) {
      const used = k.total - k.free;
      disks.append(el("div", {}, el("meter", { min: "0", max: String(k.total || 1), value: String(used), high: String(Math.round((k.total || 1) * 0.9)), "aria-label": k.name + " used" }),
        " " + k.name + " " + fmtBytes(k.free) + " free of " + fmtBytes(k.total)));
    }
    info.replaceChildren(
      el("dt", { text: "Status" }), el("dd", {}, el("span", { class: "dot" + (d.online ? " on" : "") }), " " + (d.online ? "Online" : "Offline, last seen " + ago(d.lastSeen))),
      el("dt", { text: "Last seen" }), el("dd", { text: fmtTime(d.lastSeen) }),
      el("dt", { text: "Client / site" }), el("dd", { text: d.clientName + " / " + d.siteName }),
      el("dt", { text: "Windows version" }), el("dd", { text: [d.osName, d.osVersion].filter(Boolean).join(" ") || "Not reported yet" }),
      el("dt", { text: "Uptime" }), el("dd", { text: d.uptimeSec ? fmtUptime(d.uptimeSec) : "Not reported yet" }),
      el("dt", { text: "Disk space" }), el("dd", {}, (d.disks && d.disks.length) ? disks : "Not reported yet"),
      el("dt", { text: "Signed-in user" }), el("dd", { text: d.loggedOnUser || "None reported" }),
      el("dt", { text: "Agent version" }), el("dd", { text: d.agentVersion || "Unknown" }),
      el("dt", { text: "Device ID" }), el("dd", { class: "mono", text: d.id }));
    const btn = $("connect");
    if (btn && !btn.dataset.busy) btn.disabled = !d.online;
  }

  function renderDetail() {
    const d = selected;
    const allowed = tree.perms[d.clientId] || [];
    const labels = { view: "View screen", control: "Control mouse and keyboard", clipboard: "Clipboard sync", transfer: "File transfer" };
    const perms = el("div", { class: "perms", role: "group", "aria-label": "Permissions to request" });
    for (const p of ["view", "control", "clipboard", "transfer"]) {
      const ok = allowed.includes(p);
      perms.append(el("label", {}, el("input", { type: "checkbox", name: "perm", value: p, checked: ok, disabled: !ok || p === "view" }), labels[p] + (ok ? "" : " (not allowed for you)")));
    }
    const children = [
      el("h1", { text: d.hostname || d.id }),
      el("dl", { class: "kv", id: "detail-info" }),
      el("h2", { text: "Attended support" }),
      el("p", { class: "muted", text: "The person at the PC sees your name and these permissions, types the support code you give them, and can untick any permission or end the session at any time." }),
      perms,
      el("p", {}, el("button", { type: "button", class: "primary", id: "connect", onclick: connect }, "Connect")),
      el("div", { id: "session-box" }),
    ];
    if (me.isAdmin) {
      children.push(el("h2", { text: "Device access" }),
        el("p", { class: "muted", text: "Revoking ends any session, disconnects the agent and permanently invalidates its credentials. The device must be enrolled again with a new token." }),
        el("button", { type: "button", class: "danger", onclick: () => revoke(d) }, "Revoke device"));
    }
    $("detail").replaceChildren(...children);
    renderDetailInfo();
  }

  async function revoke(d) {
    if (!window.confirm("Revoke " + (d.hostname || d.id) + "? This cannot be undone.")) return;
    try { await api("POST", "/api/admin/devices/" + encodeURIComponent(d.id) + "/revoke"); selected = null; $("detail").replaceChildren(el("p", { class: "muted", text: "Device revoked." })); loadTree(); }
    catch (e) { window.alert(e.message); }
  }

  async function connect() {
    const btn = $("connect");
    const perms = [...document.querySelectorAll("input[name=perm]:checked")].map((i) => i.value);
    btn.disabled = true; btn.dataset.busy = "1";
    const box = $("session-box");
    try {
      const r = await api("POST", "/api/devices/" + encodeURIComponent(selected.id) + "/sessions", { perms });
      const link = "rsm://connect?server=" + encodeURIComponent(location.origin) + "&sid=" + encodeURIComponent(r.sessionId) + "&ticket=" + encodeURIComponent(r.ticket);
      const state = el("p", { role: "status", text: "Waiting for the person at the PC to accept…" });
      box.replaceChildren(el("div", { class: "session" },
        el("p", { text: "Read this support code to the person at the PC:" }),
        el("p", { class: "code", text: r.code }),
        state,
        el("p", {}, el("a", { class: "primary", href: link }, "Open in viewer"), " ",
          el("button", { type: "button", class: "quiet", onclick: () => navigator.clipboard.writeText(link) }, "Copy viewer link"), " ",
          el("button", { type: "button", class: "quiet", onclick: () => endSession(r.sessionId) }, "End session")),
        el("p", { class: "muted", text: "Granted permissions: " + r.perms.join(", ") + ". The viewer link works once and expires with the request." })));
      clearInterval(poll);
      poll = setInterval(async () => {
        try {
          const s = await api("GET", "/api/sessions/" + encodeURIComponent(r.sessionId));
          if (s.state === "active") state.textContent = "Connected. Granted: " + s.granted.join(", ");
          else if (s.state === "suspended") state.textContent = "Reconnecting…";
          else if (s.state === "ended") { state.textContent = "Session ended: " + s.reason; clearInterval(poll); btn.disabled = !selected.online; delete btn.dataset.busy; }
        } catch (_) { clearInterval(poll); btn.disabled = false; delete btn.dataset.busy; }
      }, 1500);
    } catch (e) {
      box.replaceChildren(el("p", { class: "error", role: "alert", text: e.message }));
      btn.disabled = false; delete btn.dataset.busy;
    }
  }

  async function endSession(id) {
    try { await api("POST", "/api/sessions/" + encodeURIComponent(id) + "/end"); } catch (e) { window.alert(e.message); }
  }

  // ------------------------------------------------------------ admin
  function adminError(e) { $("admin-error").textContent = e ? e.message : ""; }

  async function loadAdmin() {
    adminError(null);
    const t = await api("GET", "/api/tree");
    tree = t;
    $("site-client").replaceChildren(...t.clients.map((c) => el("option", { value: String(c.id), text: c.name })));
    const sites = [];
    for (const c of t.clients) for (const s of c.sites) sites.push(el("option", { value: String(s.id), text: c.name + " / " + s.name }));
    $("enroll-site").replaceChildren(...sites);
    const u = await api("GET", "/api/admin/users");
    const box = $("users");
    box.replaceChildren();
    for (const row of u.users) {
      const user = row.user;
      const div = el("div", { class: "user" },
        el("strong", { text: user.name }), " ", el("span", { class: "muted", text: user.email }), " ",
        user.isAdmin ? el("span", { class: "badge", text: "administrator" }) : null, " ",
        user.disabled ? el("span", { class: "badge", text: "disabled" }) : null, " ",
        user.mfaEnrolled ? null : el("span", { class: "badge", text: "MFA not set up" }));
      if (!user.isAdmin) {
        for (const c of t.clients) {
          const cur = (row.assignments || {})[c.id] || [];
          const g = el("div", { class: "perms" }, el("span", { class: "muted", text: c.name + ":" }));
          for (const p of ["view", "control", "clipboard", "transfer"]) {
            g.append(el("label", {}, el("input", { type: "checkbox", checked: cur.includes(p), onchange: async (ev) => {
              const boxes = [...g.querySelectorAll("input")];
              const perms = boxes.filter((b) => b.checked).map((b) => b.dataset.p);
              try { await api("POST", "/api/admin/users/" + user.id + "/assignments", { clientId: c.id, perms }); adminError(null); }
              catch (e) { ev.target.checked = !ev.target.checked; adminError(e); }
            }, "data-p": p }), p));
          }
          div.append(g);
        }
      }
      if (user.id !== me.id) {
        div.append(el("p", {},
          el("button", { type: "button", class: "quiet", onclick: async () => { await api("POST", "/api/admin/users/" + user.id + "/disable", { disabled: !user.disabled }); loadAdmin(); } }, user.disabled ? "Enable" : "Disable"), " ",
          el("button", { type: "button", class: "quiet", onclick: async () => { if (window.confirm("Reset MFA for " + user.email + "? They will set it up again at next sign-in.")) { await api("POST", "/api/admin/users/" + user.id + "/reset-mfa"); loadAdmin(); } } }, "Reset MFA")));
      }
      box.append(div);
    }
  }

  $("client-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    try { await api("POST", "/api/admin/clients", { name: ev.target.elements.name.value }); ev.target.reset(); loadAdmin(); } catch (e) { adminError(e); }
  });
  $("site-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    try { await api("POST", "/api/admin/sites", { clientId: Number(ev.target.elements.clientId.value), name: ev.target.elements.name.value }); ev.target.reset(); loadAdmin(); } catch (e) { adminError(e); }
  });
  $("enroll-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    try {
      const r = await api("POST", "/api/admin/enroll-tokens", { siteId: Number(ev.target.elements.siteId.value) });
      $("enroll-out").hidden = false;
      $("enroll-cmd").textContent = "RemoteSupportAgent.exe enroll --server " + location.origin + " --token " + r.token + (pin ? " --pin " + pin : "");
    } catch (e) { adminError(e); }
  });
  $("user-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const f = ev.target.elements;
    try { await api("POST", "/api/admin/users", { name: f.name.value, email: f.email.value, password: f.password.value, admin: f.admin.checked }); ev.target.reset(); loadAdmin(); } catch (e) { adminError(e); }
  });

  // ------------------------------------------------------------ audit
  async function loadAudit() {
    const r = await api("GET", "/api/audit?limit=300");
    $("audit-rows").replaceChildren(...r.entries.map((e) => el("tr", {},
      el("td", { text: fmtTime(e.at) }), el("td", { text: e.actor }), el("td", { text: e.action }),
      el("td", { class: "mono", text: e.deviceId }), el("td", { class: "details", text: JSON.stringify(e.details) }))));
  }

  start();
})();
