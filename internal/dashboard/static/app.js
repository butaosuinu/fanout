"use strict";
/* fanout dashboard — read-only renderer.
 * Subscribes to the SSE stream; falls back to polling /api/snapshot if the
 * stream drops. The token (when the server requires one) rides as a ?token=
 * query param, read from this page's own URL. No build step, no dependencies. */

const token = new URLSearchParams(location.search).get("token") || "";
const q = (path) => path + (token ? "?token=" + encodeURIComponent(token) : "");
const $ = (id) => document.getElementById(id);

const state = { snap: null, sortKey: "issueNum", sortDir: 1, filter: "" };

/* ---- derived helpers ---- */
function prState(prs) {
  if (!prs || !prs.length) return "none";
  if (prs.some((p) => p.state === "MERGED")) return "MERGED";
  if (prs.some((p) => p.state === "OPEN")) return "OPEN";
  return prs[0].state || "none";
}
function sortValue(pane, key) {
  switch (key) {
    case "issueNum": return pane.issueNum;
    case "name": return (pane.displayName || pane.slug || "").toLowerCase();
    case "agent": return (pane.agent || "").toLowerCase();
    case "branch": return (pane.branchName || "").toLowerCase();
    case "alive": return pane.alive ? 1 : 0;
    case "issueState": return (pane.issueState || "").toLowerCase();
    case "pr": return prState(pane.prs);
    default: return 0;
  }
}
function matches(pane, f) {
  if (!f) return true;
  return [pane.issueNum, pane.displayName, pane.slug, pane.agent, pane.branchName, pane.issueState]
    .map((v) => String(v == null ? "" : v).toLowerCase())
    .some((s) => s.includes(f));
}
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function clock(iso) {
  const d = iso ? new Date(iso) : null;
  return d && !isNaN(d) ? d.toLocaleTimeString() : (iso || "");
}

/* ---- rendering ---- */
function draw() {
  const snap = state.snap;
  if (!snap) return;
  const r = snap.rollup || { total: 0, merged: 0, pending: 0, live: 0 };

  $("repo").textContent = snap.repo || "(repo unresolved)";
  $("repo").title = snap.projectRoot || "";
  $("s-total").textContent = r.total;
  $("s-live").textContent = r.live;
  $("s-merged").textContent = r.merged;
  $("s-pending").textContent = r.pending;
  $("hud-fill").style.width = (r.total ? (r.merged / r.total) * 100 : 0) + "%";

  const deg = snap.degraded || {};
  const msgs = [];
  if (deg.github) msgs.push("GitHub data unavailable — issue / PR columns degraded");
  if (deg.tmux) msgs.push("tmux unavailable — pane liveness unknown");
  // A state-load failure sets only degraded.reason (no github/tmux flag); surface
  // it so a corrupted .fanout/state.json shows a warning, not a silent empty view.
  if (deg.reason && !deg.github && !deg.tmux) msgs.push("State unavailable: " + deg.reason);
  const banner = $("banner");
  if (msgs.length) { banner.hidden = false; banner.textContent = msgs.join("   ·   "); }
  else banner.hidden = true;

  const f = state.filter.trim().toLowerCase();
  const main = $("sessions");
  main.innerHTML = "";
  const tpl = $("tpl-session");

  let shown = 0, totalPanes = 0;
  for (const s of snap.sessions || []) {
    const panes = (s.panes || []).filter((p) => matches(p, f));
    totalPanes += (s.panes || []).length;
    if (!panes.length) continue;
    shown += panes.length;

    panes.sort((a, b) => {
      const va = sortValue(a, state.sortKey), vb = sortValue(b, state.sortKey);
      if (va < vb) return -1 * state.sortDir;
      if (va > vb) return 1 * state.sortDir;
      return (a.issueNum - b.issueNum); // stable secondary
    });

    const node = tpl.content.cloneNode(true);
    // Issue-mode parents are numbers (#142); Project-mode parents are a Projects
    // v2 URL, which must not get a "#" prefix.
    node.querySelector(".parent").textContent = /^\d+$/.test(s.parent) ? "#" + s.parent : s.parent;
    const sr = s.rollup || { merged: 0, total: panes.length };
    const pct = sr.total ? (sr.merged / sr.total) * 100 : 0;
    node.querySelector(".progress-fill").style.width = pct + "%";
    node.querySelector(".progress-label").textContent = `${sr.merged} / ${sr.total} merged`;

    const tbody = node.querySelector("tbody");
    tbody.innerHTML = panes.map(rowHtml).join("");
    markSort(node.querySelectorAll("thead th"));
    main.appendChild(node);
  }

  if (!shown) {
    main.innerHTML = `<div class="empty"><span class="big">${
      totalPanes ? "no panes match filter" : "no active sessions"
    }</span>${totalPanes ? "" : "awaiting fan-out — run <code>fanout &lt;parent&gt;</code>"}</div>`;
  }

  $("filter-count").textContent = f ? `${shown} / ${totalPanes}` : "";
  $("status").textContent = "telemetry @ " + clock(snap.generatedAt);
}

function rowHtml(p) {
  const pr = prState(p.prs);
  const ist = p.issueState || "UNKNOWN";
  return `<tr>
    <td class="col-num cell-num">#${p.issueNum}</td>
    <td class="cell-name" title="${esc(p.displayName || p.slug)}">${esc(p.displayName || p.slug || "—")}</td>
    <td class="cell-agent">${esc(p.agent || "—")}</td>
    <td class="cell-branch" title="${esc(p.branchName)}">${esc(p.branchName || "—")}</td>
    <td class="col-c"><span class="dot ${p.alive ? "alive" : ""}" title="${p.alive ? "live" : "exited"}"></span></td>
    <td class="col-c"><span class="tag ${esc(ist)}">${esc(ist.toLowerCase())}</span></td>
    <td class="col-c"><span class="tag ${esc(pr)}">${pr === "none" ? "—" : esc(pr.toLowerCase())}</span></td>
  </tr>`;
}

function markSort(headers) {
  headers.forEach((th) => {
    const k = th.dataset.sort;
    if (k === state.sortKey) th.setAttribute("aria-sort", state.sortDir === 1 ? "ascending" : "descending");
    else th.removeAttribute("aria-sort");
  });
}

function onSort(key) {
  if (state.sortKey === key) state.sortDir *= -1;
  else { state.sortKey = key; state.sortDir = 1; }
  draw();
}

/* ---- connection ---- */
function setConn(up, text) {
  const link = $("link");
  link.classList.toggle("up", up);
  link.classList.toggle("down", !up);
  $("conn-text").textContent = text;
}

function render(snap) { state.snap = snap; draw(); }

/* ---- transport ---- */
let pollTimer = null;
function startPolling() {
  if (pollTimer) return;
  const tick = async () => {
    try {
      const res = await fetch(q("/api/snapshot"), { cache: "no-store" });
      if (res.ok) { setConn(true, "polling"); render(await res.json()); }
      else setConn(false, "error " + res.status);
    } catch (_) { setConn(false, "offline"); }
  };
  tick();
  pollTimer = setInterval(tick, 2000);
}
function stopPolling() { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } }

function connect() {
  if (typeof EventSource === "undefined") { startPolling(); return; }
  let es;
  try { es = new EventSource(q("/api/stream")); }
  catch (_) { startPolling(); return; }
  es.addEventListener("open", () => { stopPolling(); setConn(true, "streaming"); });
  es.addEventListener("snapshot", (e) => { setConn(true, "streaming"); render(JSON.parse(e.data)); });
  es.onerror = () => { setConn(false, "stream lost"); es.close(); startPolling(); };
}

/* ---- wiring ---- */
document.addEventListener("click", (e) => {
  const th = e.target.closest("thead th[data-sort]");
  if (th) onSort(th.dataset.sort);
});
$("filter").addEventListener("input", (e) => { state.filter = e.target.value; draw(); });

connect();
