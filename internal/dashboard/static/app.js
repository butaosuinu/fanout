"use strict";
/* fanout dashboard — read-only renderer (PAPER BREEZE).
 * Subscribes to the SSE stream; falls back to polling /api/snapshot if the
 * stream drops. The token (when the server requires one) rides as a ?token=
 * query param, read from this page's own URL. No build step, no dependencies.
 * UI logic mirrors docs/mockups/dashboard-paper-breeze.html (the approved
 * design contract); only the transport + /api/peek wiring is real here.
 * 描画はキー付き差分パッチ(reconcile): SSE 更新ごとに innerHTML を全置換せず、
 * 変化したセクション/行だけ更新する(rise 再生・hover/フォーカス喪失の防止)。 */

const token = new URLSearchParams(location.search).get("token") || "";
const q = (path) => path + (token ? "?token=" + encodeURIComponent(token) : "");
const $ = (id) => document.getElementById(id);

const state = { snap: null, sortKey: "issueNum", sortDir: 1, filter: "", selected: null };

/* ---- helpers ---- */
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const cssEsc = (s) => (window.CSS && CSS.escape ? CSS.escape(String(s)) : String(s).replace(/["\\]/g, "\\$&"));

/* ---- GitHub リンク ---- */
/* snap.repo("owner/name")を検証してリポジトリ URL の基底を返す。形式不一致
 * (空・予期しない文字)なら "" — その場合リンク化せず素テキストに落とす。 */
function ghBase() {
  const repo = state.snap?.repo || "";
  return /^[\w.-]+\/[\w.-]+$/.test(repo) ? "https://github.com/" + repo : "";
}
function issueUrl(n) {
  const base = ghBase();
  return base && n ? `${base}/issues/${n}` : "";
}
function prUrl(n) {
  const base = ghBase();
  return base && n ? `${base}/pull/${n}` : "";
}
/* Issue-mode parents are plain numbers; Project-mode parents are already a
 * github.com Projects URL(プレフィックス検証つきパススルー)。それ以外は "". */
function parentUrl(parent) {
  if (/^\d+$/.test(parent)) return issueUrl(parent);
  if (/^https:\/\/github\.com\//.test(parent)) return parent;
  return "";
}
/* inner は呼び出し側で esc 済みの HTML だけを渡すこと。href は esc() を通す
 * (Projects URL パススルー対策)。url が空なら inner をそのまま返す。token は
 * index.html の <meta name="referrer" content="no-referrer"> により漏れない。 */
function linkHtml(url, inner, cls) {
  if (!url) return inner;
  return `<a class="${cls || "gh"}" href="${esc(url)}" target="_blank" rel="noopener noreferrer">${inner}</a>`;
}

/* ghissue.PrimaryPR と同じ選択規則(MERGED 優先、なければ先頭)。ciStatus と
 * 同じ PR を指すよう backend とミラーしておかないと、PR 列と ci 列が別の PR を
 * 表示してしまう。 */
function prPrimary(prs) {
  if (!prs || !prs.length) return null;
  return prs.find((p) => p.state === "MERGED") || prs[0];
}
function ciWorst(prs) {
  const rank = { fail: 3, pending: 2, pass: 1 };
  let worst = "";
  for (const pr of prs || []) {
    const c = (pr.ci || "").toLowerCase();
    if ((rank[c] || 0) > (rank[worst] || 0)) worst = c;
  }
  return worst;
}
/* Pane-level CI — the wire's p.ciStatus (primary-PR CI, lowercase, "-" when
 * the primary PR has no CI) so the dashboard agrees with the TUI. "-" means
 * "no CI", not "unknown": falling back to worst-of-prs there would surface a
 * non-primary PR's failure. The fallback is only for snapshots predating the
 * field entirely. */
function paneCI(p) {
  if (p.ciStatus == null || p.ciStatus === "") return ciWorst(p.prs);
  return p.ciStatus === "-" ? "" : p.ciStatus;
}
/* Mirrors Go blockers.FormatStatuses: OPEN → "OPEN #N", CLOSED → "resolved #N",
 * anything else (UNKNOWN etc.) → "<STATE> #N". blockerLabel はその #N を除いた
 * 状態ラベル部分(プレーンテキスト): fmtBlockers(title 属性・検索 haystack)と
 * ドロワーのリンク化 HTML(blockersHtml)で共用する。 */
function blockerLabel(b) {
  if (b.state === "OPEN") return "OPEN";
  if (b.state === "CLOSED") return "resolved";
  return String(b.state ?? "").trim() || "-";
}
function fmtBlockers(p) {
  if (!p.blockers || !p.blockers.length) return "-";
  return p.blockers.map((b) => `${blockerLabel(b)} #${b.num}`).join(", ");
}
/* ドロワー用: fmtBlockers と同表記のまま各 #N を issue リンクに(repo 未解決時
 * は linkHtml が素テキストに落とす)。 */
function blockersHtml(p) {
  if (!p.blockers || !p.blockers.length) return "-";
  return p.blockers.map((b) =>
    `${esc(blockerLabel(b))} ${linkHtml(issueUrl(b.num), "#" + esc(b.num))}`).join(", ");
}
function fmtWave(p) { return p.waveLabel || (p.wave ? `w${p.wave}` : ""); }
function clock(iso) {
  const d = new Date(iso);
  return isNaN(d) ? "--:--:--" : d.toTimeString().slice(0, 8);
}

/* 構造化フィルタ: key:value + 自由語、すべて AND。未知キーは自由語に降格 */
const FILTER_KEYS = new Set(["state", "agent", "wave", "ci", "dirty", "live", "issue", "pr"]);
function parseQuery(str) {
  const terms = [];
  for (const tok of String(str).trim().split(/\s+/)) {
    if (!tok) continue;
    const m = /^([a-z]+):(\S*)$/i.exec(tok);
    if (m && FILTER_KEYS.has(m[1].toLowerCase())) terms.push({ key: m[1].toLowerCase(), value: m[2].toLowerCase() });
    else terms.push({ word: tok.toLowerCase() });
  }
  return terms;
}
function matches(p, terms) {
  const hay = [p.issueNum, p.displayName, p.slug, p.agent, p.branchName, p.diffSummary,
    p.dirtyState, p.issueState, p.tmuxTitle, fmtWave(p), fmtBlockers(p)].join(" ").toLowerCase();
  for (const t of terms) {
    if (t.word) { if (!hay.includes(t.word)) return false; continue; }
    const pr = prPrimary(p.prs);
    switch (t.key) {
      case "state": if ((p.tmuxState === t.value ? p.tmuxState : String(p.issueState || "").toLowerCase()) !== t.value) return false; break;
      case "agent": if (!String(p.agent || "").toLowerCase().includes(t.value)) return false; break;
      case "wave": if (String(p.wave ?? "") !== t.value && (p.waveLabel || "").toLowerCase() !== t.value && fmtWave(p).toLowerCase() !== t.value) return false; break;
      case "ci": if (paneCI(p) !== t.value) return false; break;
      case "dirty": if ((t.value === "yes") !== (p.dirtyState === "dirty")) return false; break;
      case "live": if ((t.value === "yes") !== !!p.alive) return false; break;
      case "issue": if (String(p.issueNum) !== t.value) return false; break;
      case "pr": if (((pr && pr.state) || "none").toLowerCase() !== t.value) return false; break;
    }
  }
  return true;
}

const SORTS = {
  issueNum: (p) => p.issueNum ?? 0,
  name: (p) => String(p.displayName || p.slug || "").toLowerCase(),
  agent: (p) => String(p.agent || "").toLowerCase(),
  wave: (p) => p.wave || 99,
  blockers: (p) => (p.blockers || []).filter((b) => b.state === "OPEN").length,
  branch: (p) => String(p.branchName || "").toLowerCase(),
  diff: (p) => { const m = /\+(\d+)\/-(\d+)/.exec(p.diffSummary || ""); return m ? +m[1] + +m[2] : -1; },
  dirty: (p) => ({ clean: 0, dirty: 1, unknown: 2 }[p.dirtyState] ?? 3),
  ci: (p) => ({ fail: 0, pending: 1, pass: 2 }[paneCI(p)] ?? 3),
  tmux: (p) => (p.alive ? 0 : 1),
  state: (p) => String(p.issueState || "").toLowerCase(),
  pr: (p) => { const pr = prPrimary(p.prs); return { MERGED: 0, OPEN: 1, CLOSED: 2 }[pr && pr.state] ?? 3; },
};

/* ---- 描画 ---- */
/* 直前に書き込んだ HTML 文字列と一致するなら DOM を触らない。DOM の再シリア
 * ライズは round-trip しないので、比較は常に「自分が書いた文字列」と行う。 */
const htmlCache = new WeakMap();
function setHtml(el, html) {
  if (htmlCache.get(el) === html) return false;
  el.innerHTML = html;
  htmlCache.set(el, html);
  return true;
}
/* key 付き子要素のカーソル走査 reconcile。順序どおりに既存ノードを再利用し、
 * ずれたノードは insertBefore で移動(要素の状態が保たれちらつかない)、無い
 * ものは make() で生成、ループ後に余った子を remove する。 */
function reconcile(container, items, keyOf, make, patch) {
  let cursor = container.firstElementChild;
  for (const item of items) {
    let node = cursor && keyOf(cursor) === item.key ? cursor : null;
    if (!node) {
      for (let n = cursor; n; n = n.nextElementSibling) {
        if (keyOf(n) === item.key) { node = n; break; }
      }
      if (!node) node = make(item);
      container.insertBefore(node, cursor);
    }
    patch(node, item);
    cursor = node.nextElementSibling;
  }
  while (cursor) { const gone = cursor; cursor = cursor.nextElementSibling; gone.remove(); }
}
function tagHtml(cls, label, title) {
  return `<span class="tag ${cls}"${title ? ` title="${esc(title)}"` : ""}>${esc(label)}</span>`;
}
/* blockers セル: "resolved" は全 blocker が CLOSED 確定のときだけ。state 取得に
 * 失敗した UNKNOWN 行が混ざる場合は unknown と表示する(解決済みと誤認させない)。 */
function blockersCell(p, openBlk) {
  if (p.blocked) return tagHtml("t-warn", openBlk + " open");
  if (!p.blockers || !p.blockers.length) return '<span class="muted">—</span>';
  const allClosed = p.blockers.every((b) => b.state === "CLOSED");
  return allClosed ? '<span class="muted">resolved</span>' : '<span class="muted">unknown</span>';
}
/* 行の安定キー。tmux 再起動後は pane id (%N) が別 issue の古い行と重複しうる
 * ので、選択は parent#issueNum で識別し、paneId は capture 対象にだけ使う。 */
function rowKey(parent, p) {
  return `${parent}#${p.issueNum}`;
}
/* 12 <td> セルのみ。tr 自体は makeRow が生成し、selected クラスは
 * classList 操作専用(markup には含めない)。 */
function rowCellsHtml(p) {
  const pr = prPrimary(p.prs);
  const ci = paneCI(p);
  const openBlk = (p.blockers || []).filter((b) => b.state === "OPEN").length;
  return `<td class="c-issue">${linkHtml(issueUrl(p.issueNum), "#" + esc(p.issueNum))}</td>
    <td class="c-name" title="${esc(p.slug)}">${esc(p.displayName || p.slug || "—")}</td>
    <td>${esc(p.agent || "—")}</td>
    <td>${fmtWave(p) ? esc(fmtWave(p)) : '<span class="muted">—</span>'}</td>
    <td title="${esc(fmtBlockers(p))}">${blockersCell(p, openBlk)}</td>
    <td class="c-branch" title="${esc(p.branchName)}">${esc(p.branchName || "—")}</td>
    <td class="c-diff${p.worktreeErr ? " fault" : ""}" title="${esc(p.worktreeErr || "")}">${diffHtml(p)}</td>
    <td>${p.dirtyState === "clean" ? tagHtml("t-ok", "clean") : p.dirtyState === "dirty" ? tagHtml("t-warn", "dirty") : '<span class="muted">—</span>'}</td>
    <td>${ci === "pass" ? tagHtml("t-ok", "pass") : ci === "fail" ? tagHtml("t-err", "fail") : ci === "pending" ? tagHtml("t-warn", "pending") : '<span class="muted">—</span>'}</td>
    <td title="${esc(p.tmuxTitle || "")}"><span class="dot ${p.alive ? "on" : "off"}" aria-hidden="true"></span>${p.alive ? "live" : esc(p.tmuxState || "stale")}</td>
    <td>${p.issueState === "OPEN" ? tagHtml("t-open", "OPEN") : p.issueState === "CLOSED" ? tagHtml("", "CLOSED") : '<span class="muted">?</span>'}</td>
    <td>${prCell(pr)}</td>`;
}
function diffHtml(p) {
  const m = /^\+(\d+)\/-(\d+)$/.exec(p.diffSummary || "");
  if (!m) return `<span class="muted">${esc(p.diffSummary || "—")}</span>`;
  return `<span class="add">+${m[1]}</span>/<span class="del">-${m[2]}</span>`;
}
function prCell(pr) {
  if (!pr) return '<span class="muted">—</span>';
  const cls = pr.state === "MERGED" ? "t-merged" : pr.state === "OPEN" ? "t-open" : "";
  const draft = pr.isDraft ? " t-draft" : "";
  // PRRef carries no title on the wire (ghissue.PRRef) — number/state only.
  // 行の PR 列・ドロワーの PR リスト共通で、ピルごと PR ページへのリンクに包む。
  return linkHtml(prUrl(pr.number), tagHtml(cls + draft, `#${pr.number} ${pr.isDraft ? "draft" : pr.state}`), "gh gh-pill");
}

const COLS = [
  ["issueNum", "issue"], ["name", "name"], ["agent", "agent"], ["wave", "wave"],
  ["blockers", "blockers"], ["branch", "branch"], ["diff", "diff"], ["dirty", "dirty"],
  ["ci", "ci"], ["tmux", "tmux"], ["state", "state"], ["pr", "pr"],
];

function draw(animate) {
  const snap = state.snap;
  if (!snap) return;

  $("repo").textContent = snap.repo || "(repo unresolved)";
  $("repo").title = snap.projectRoot || "";

  const deg = snap.degraded || {};
  const msgs = [];
  if (deg.github) msgs.push("GitHub データ取得が不安定 — issue / PR / CI 列は劣化表示");
  if (deg.tmux) msgs.push("tmux が利用できません — ペイン生死・peek は劣化表示");
  // A state-load failure sets only degraded.reason (no github/tmux flag); surface
  // it so a corrupted .fanout/state.json shows a warning, not a silent empty view.
  if (deg.reason && !deg.github && !deg.tmux) msgs.push("state 読み込みに失敗: " + deg.reason);
  const banner = $("banner");
  banner.hidden = !msgs.length;
  banner.textContent = msgs.join(" · ");

  renderSessions(snap, animate);

  const r = snap.rollup || { total: 0, merged: 0, pending: 0, live: 0, blocked: 0 };
  $("s-total").textContent = r.total; $("s-live").textContent = r.live;
  $("s-merged").textContent = r.merged; $("s-pending").textContent = r.pending;
  $("s-blocked").textContent = r.blocked;
  $("hud-fill").style.width = (r.total ? (r.merged / r.total) * 100 : 0) + "%";
  $("status").textContent = "telemetry @ " + clock(snap.generatedAt);
  syncDrawer();
}

/* セッション一覧の差分パッチ描画。filter+sort 済みの順序付きリストを作り、
 * #sessions の子(section[data-parent] でキー付け)へ reconcile する。空状態
 * (該当なし)も key="" の専用エントリとして同じループで扱う。animate===true
 * (SSE 更新)のときだけ新規セクションに rise 入場アニメを付与する。 */
function renderSessions(snap, animate) {
  if (!snap) return;
  const terms = parseQuery(state.filter);
  const thsHtml = COLS.map(([key, label]) =>
    `<th data-sort="${key}" aria-sort="${state.sortKey === key ? (state.sortDir === 1 ? "ascending" : "descending") : "none"}">${label}${state.sortKey === key ? ` <span class="dir">${state.sortDir === 1 ? "▴" : "▾"}</span>` : ""}</th>`).join("");
  let shown = 0, total = 0;
  const items = [];
  for (const s of snap.sessions || []) {
    const all = s.panes || [];
    total += all.length;
    const panes = all.filter((p) => matches(p, terms));
    shown += panes.length;
    if (!panes.length) continue;
    panes.sort((a, b) => {
      const ka = SORTS[state.sortKey](a), kb = SORTS[state.sortKey](b);
      if (ka < kb) return -state.sortDir;
      if (ka > kb) return state.sortDir;
      return (a.issueNum ?? 0) - (b.issueNum ?? 0); // stable secondary
    });
    // Issue-mode parents are numbers (#142); Project-mode parents are a Projects
    // v2 URL, which must not get a "#" prefix.
    const parent = String(s.parent || "");
    const parentLabel = /^\d+$/.test(parent) ? "#" + parent : parent.replace(/^https:\/\/github\.com\//, "");
    const sr = s.rollup || { total: all.length, merged: 0 };
    const pct = sr.total ? Math.round((sr.merged / sr.total) * 100) : 0;
    items.push({
      key: parent,
      headHtml: `<h2><span class="s-parent">${linkHtml(parentUrl(parent), esc(parentLabel))}</span></h2>
        <div class="s-progress"><span>${sr.merged}/${sr.total} merged</span><div class="bar"><i style="width:${pct}%"></i></div></div>`,
      thsHtml,
      rows: panes.map((p) => ({ key: rowKey(parent, p), cells: rowCellsHtml(p) })),
    });
  }
  if (!items.length) {
    items.push({ key: "", emptyHtml: `<div class="empty"><span class="ji">no panes in the breeze</span>${total ? "フィルタに一致するペインがありません" : "アクティブなセッションがありません"}</div>` });
  }
  reconcile($("sessions"), items, (n) => n.dataset.parent, (item) => makeSection(item, animate), patchSection);
  $("filter-count").textContent = `${shown} / ${total}`;
}
/* 空状態(emptyHtml 持ち・key="")は .empty 1 枚のフラット構造で、従来どおり
 * rise なし。通常セクションはスケルトンを作り patchSection が中身を埋める。 */
const SECTION_SKELETON = '<header class="session-head"></header><div class="table-wrap"><table><thead><tr></tr></thead><tbody></tbody></table></div>';
function makeSection(item, animate) {
  const sec = document.createElement("section");
  sec.className = "session" + (animate && item.emptyHtml == null ? " rise" : "");
  sec.dataset.parent = item.key;
  if (item.emptyHtml == null) setHtml(sec, SECTION_SKELETON);
  return sec;
}
function patchSection(sec, item) {
  if (item.emptyHtml != null) { setHtml(sec, item.emptyHtml); return; }
  // 空状態ノードが同キーで再利用された場合(degenerate な parent="")に備え、
  // スケルトン欠落時は組み直す。
  if (!sec.querySelector(".session-head")) setHtml(sec, SECTION_SKELETON);
  setHtml(sec.querySelector(".session-head"), item.headHtml);
  setHtml(sec.querySelector("thead tr"), item.thsHtml); // ソート変更時のみ変化
  reconcile(sec.querySelector("tbody"), item.rows, (n) => n.dataset.key, makeRow, patchRow);
}
function makeRow(item) {
  const tr = document.createElement("tr");
  tr.className = "row"; // 行には rise を付けない — セクションのみ
  tr.dataset.key = item.key;
  tr.tabIndex = 0;
  return tr;
}
function patchRow(tr, item) {
  setHtml(tr, item.cells);
  tr.classList.toggle("selected", state.selected === item.key);
}

/* ---- 詳細ドロワー + peek ---- */
function findPane(key) {
  for (const s of state.snap?.sessions || []) {
    const parent = String(s.parent || "");
    for (const p of s.panes || []) if (rowKey(parent, p) === key) return p;
  }
  return null;
}
function rowFor(key) {
  return key ? document.querySelector(`tr.row[data-key="${cssEsc(key)}"]`) : null;
}
/* draw() の末尾で選択中ペインを再描画。snapshot から消えたら通知して閉じる */
function syncDrawer() {
  if (!state.selected) { renderDrawer(null); return; }
  const p = findPane(state.selected);
  if (p) { renderDrawer(p); return; }
  const gone = state.selected;
  state.selected = null;
  renderDrawer(null);
  $("status").textContent = `${gone} は snapshot から消えたため詳細を閉じました · ` + $("status").textContent;
}
function renderDrawer(p) {
  const drawer = $("drawer");
  if (!p) { stopPeek(); drawer.hidden = true; return; }
  drawer.hidden = false;
  setHtml($("d-issue"), linkHtml(issueUrl(p.issueNum), "#" + esc(p.issueNum)));
  $("d-name").textContent = p.displayName || p.slug || "—";
  $("d-agent").textContent = p.agent || "—";
  $("d-pane").textContent = p.paneId || "—";
  $("d-tmux").textContent = p.alive ? "live" : (p.tmuxState || "stale");
  $("d-title").textContent = p.tmuxTitle || "—";
  $("d-created").textContent = p.createdAt ? p.createdAt.replace("T", " ").slice(0, 16) : "—";
  setHtml($("d-state"), p.issueState === "OPEN" ? tagHtml("t-open", "OPEN") : p.issueState === "CLOSED" ? tagHtml("", "CLOSED") : '<span class="muted">UNKNOWN</span>');
  $("d-wave").textContent = fmtWave(p) || "—";
  setHtml($("d-blockers"), blockersHtml(p));
  $("d-path").textContent = (p.worktreePath || "—") + (p.worktreeErr ? ` (${p.worktreeErr})` : "");
  $("d-branch").textContent = p.branchName || "—";
  $("d-diff").textContent = p.diffSummary || "—";
  setHtml($("d-dirty"), p.dirtyState === "clean" ? tagHtml("t-ok", "clean") : p.dirtyState === "dirty" ? tagHtml("t-warn", "dirty") : '<span class="muted">unknown</span>');
  setHtml($("d-prs"), (p.prs && p.prs.length)
    ? p.prs.map((pr) => `<li>${prCell(pr)}${pr.ci ? " " + (pr.ci === "pass" ? tagHtml("t-ok", "ci pass") : pr.ci === "fail" ? tagHtml("t-err", "ci fail") : tagHtml("t-warn", "ci pending")) : ""}${pr.reviewDecision ? " " + tagHtml("", pr.reviewDecision.toLowerCase().replace(/_/g, " ")) : ""}</li>`).join("")
    : '<li class="muted">—</li>');
  $("d-prompt").textContent = p.prompt || "—";
  $("peek-title").textContent = `${p.paneId} — ${p.agent || "?"}`;
  startPeek(p);
}

/* peek: 開いている間、生きているペインだけを 5s ごとに /api/peek へ。
 * capture 出力は敵性入力 — textContent 以外で描画しないこと。 */
const peek = { paneId: null, timer: null, ctrl: null };

function stopPeek() {
  if (peek.timer) { clearInterval(peek.timer); peek.timer = null; }
  if (peek.ctrl) { peek.ctrl.abort(); peek.ctrl = null; }
  peek.paneId = null;
}
function startPeek(p) {
  if (!p.alive) {
    stopPeek();
    $("peek-pre").textContent = "(pane output unavailable — ペインは終了しています)";
    $("peek-meta").textContent = "—";
    return;
  }
  if (peek.paneId === p.paneId && peek.timer) return; // already polling this pane
  stopPeek();
  peek.paneId = p.paneId;
  fetchPeek(p.paneId);
  peek.timer = setInterval(() => fetchPeek(p.paneId), 5000);
}
async function fetchPeek(paneId) {
  if (peek.ctrl) peek.ctrl.abort();
  const ctrl = new AbortController();
  peek.ctrl = ctrl;
  const params = new URLSearchParams({ pane: paneId });
  if (token) params.set("token", token);
  try {
    const res = await fetch("/api/peek?" + params.toString(), { cache: "no-store", signal: ctrl.signal });
    if (peek.paneId !== paneId) return; // switched away while in flight
    if (!res.ok) {
      $("peek-pre").textContent = "(pane output unavailable)";
      $("peek-meta").textContent = "—";
      return;
    }
    const body = await res.json();
    if (peek.paneId !== paneId) return;
    $("peek-pre").textContent = body.output || "";
    $("peek-meta").textContent = `captured ${clock(body.capturedAt)} · ${body.lines} lines · 5s ごとに更新`;
  } catch (err) {
    if (err && err.name === "AbortError") return;
    if (peek.paneId !== paneId) return;
    $("peek-pre").textContent = "(pane output unavailable)";
    $("peek-meta").textContent = "—";
  }
}

/* 選択の変更は再描画しない — selected クラスの付け替えだけ行う */
function updateSelection() {
  for (const tr of document.querySelectorAll("tr.row.selected")) tr.classList.remove("selected");
  const tr = rowFor(state.selected);
  if (tr) tr.classList.add("selected");
}
function openDrawer(key) {
  state.selected = key;
  updateSelection();
  syncDrawer();
  rowFor(key)?.focus();
}
function closeDrawer() {
  const key = state.selected;
  state.selected = null;
  updateSelection();
  renderDrawer(null); // hides the drawer and stops the peek
  rowFor(key)?.focus(); // restore focus to the originating row
}

/* 行クリック/ソートはイベント委譲 — 描画ごとのリスナー張り直しをしない */
$("sessions").addEventListener("click", (e) => {
  if (e.target.closest("a")) return; // GitHub リンク(issue / PR / 親)は行選択にしない
  const th = e.target.closest("th[data-sort]");
  if (th) {
    const k = th.dataset.sort;
    if (state.sortKey === k) state.sortDir = -state.sortDir;
    else { state.sortKey = k; state.sortDir = 1; }
    renderSessions(state.snap, false);
    return;
  }
  const tr = e.target.closest("tr.row");
  if (tr) openDrawer(tr.dataset.key);
});
$("sessions").addEventListener("keydown", (e) => {
  if (e.key !== "Enter" && e.key !== " ") return;
  if (e.target.matches && e.target.matches("tr.row")) { e.preventDefault(); openDrawer(e.target.dataset.key); }
});

/* ---- テーマ(FOUC ブートストラップは index.html 側) ---- */
function applyThemeButton() {
  const t = document.documentElement.dataset.theme;
  $("theme-toggle").setAttribute("aria-pressed", t === "dark" ? "true" : "false");
}
$("theme-toggle").addEventListener("click", () => {
  const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  try { localStorage.setItem("fanout.theme", next); } catch (e) { /* private mode */ }
  applyThemeButton();
});
matchMedia("(prefers-color-scheme: dark)").addEventListener("change", (e) => {
  try { if (localStorage.getItem("fanout.theme")) return; } catch (err) { /* ignore */ }
  document.documentElement.dataset.theme = e.matches ? "dark" : "light";
  applyThemeButton();
});

/* ---- connection ---- */
function setConn(up, text) {
  const link = $("link");
  const pulse = link.querySelector(".pulse");
  if (pulse) pulse.classList.toggle("down", !up);
  const label = link.querySelector(".conn-label");
  if (label) label.textContent = text;
}

/* SSE/ポーリング更新は animate=true(定常 tick は文字列一致で no-op)。
 * フィルタ入力・ソートクリックは animate=false(rise 再生なし)。 */
function render(snap) { state.snap = snap; draw(true); }

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
  setConn(true, "linking…");
  if (typeof EventSource === "undefined") { startPolling(); return; }
  let es;
  try { es = new EventSource(q("/api/stream")); }
  catch (_) { startPolling(); return; }
  es.addEventListener("open", () => { stopPolling(); setConn(true, "streaming"); });
  es.addEventListener("snapshot", (e) => { setConn(true, "streaming"); render(JSON.parse(e.data)); });
  es.onerror = () => { setConn(false, "stream lost"); es.close(); startPolling(); };
}

/* ---- wiring ---- */
$("filter").addEventListener("input", (e) => { state.filter = e.target.value; renderSessions(state.snap, false); });
$("drawer-close").addEventListener("click", closeDrawer);
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && state.selected) closeDrawer(); });

applyThemeButton();
connect();
