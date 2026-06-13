import type { BlockerStatus, PaneView, PRRef, Snapshot } from "./types";

/* ghissue.PrimaryPR と同じ選択規則(MERGED 優先、なければ先頭)。ciStatus と
 * 同じ PR を指すよう backend とミラーしておかないと、PR 列と ci 列が別の PR を
 * 表示してしまう。 */
export function prPrimary(prs: PRRef[] | null | undefined): PRRef | null {
  if (!prs || !prs.length) return null;
  return prs.find((p) => p.state === "MERGED") ?? prs[0] ?? null;
}

const CI_RANK: Record<string, number> = { fail: 3, pending: 2, pass: 1 };

export function ciWorst(prs: PRRef[] | null | undefined): string {
  let worst = "";
  for (const pr of prs ?? []) {
    const c = (pr.ci ?? "").toLowerCase();
    if ((CI_RANK[c] ?? 0) > (CI_RANK[worst] ?? 0)) worst = c;
  }
  return worst;
}

/* Pane-level CI — the wire's p.ciStatus (primary-PR CI, lowercase, "-" when
 * the primary PR has no CI) so the dashboard agrees with the TUI. "-" means
 * "no CI", not "unknown": falling back to worst-of-prs there would surface a
 * non-primary PR's failure. The fallback is only for snapshots predating the
 * field entirely. */
export function paneCI(p: PaneView): string {
  if (p.ciStatus == null || p.ciStatus === "") return ciWorst(p.prs);
  return p.ciStatus === "-" ? "" : p.ciStatus;
}

/* Mirrors Go blockers.FormatStatuses: OPEN → "OPEN #N", CLOSED → "resolved #N",
 * anything else (UNKNOWN etc.) → "<STATE> #N". */
export function blockerLabel(b: BlockerStatus): string {
  if (b.state === "OPEN") return "OPEN";
  if (b.state === "CLOSED") return "resolved";
  return String(b.state ?? "").trim() || "-";
}

export function fmtBlockers(p: PaneView): string {
  if (!p.blockers || !p.blockers.length) return "-";
  return p.blockers.map((b) => `${blockerLabel(b)} #${b.num}`).join(", ");
}

export function fmtWave(p: PaneView): string {
  return p.waveLabel || (p.wave ? `w${p.wave}` : "");
}

export function openBlockerCount(p: PaneView): number {
  return (p.blockers ?? []).filter((b) => b.state === "OPEN").length;
}

/* blockers セルの "resolved" は全 blocker が CLOSED 確定のときだけ。state 取得に
 * 失敗した UNKNOWN 行が混ざる場合は解決済みと誤認させない。 */
export function blockersAllClosed(p: PaneView): boolean {
  return !!p.blockers && p.blockers.length > 0 && p.blockers.every((b) => b.state === "CLOSED");
}

/* 行の安定キー。tmux 再起動後は pane id (%N) が別 issue の古い行と重複しうる
 * ので、選択は parent#issueNum で識別し、paneId は capture 対象にだけ使う。 */
export function rowKey(parent: string, p: PaneView): string {
  return `${parent}#${p.issueNum}`;
}

export function findPane(snap: Snapshot | null, key: string | null): PaneView | null {
  if (!snap || !key) return null;
  for (const s of snap.sessions ?? []) {
    const parent = String(s.parent ?? "");
    for (const p of s.panes ?? []) if (rowKey(parent, p) === key) return p;
  }
  return null;
}

/* 未開始(synthetic)行の Drawer 状態説明文。キーは tmuxState。 */
const NOT_STARTED_NOTES: Record<string, string> = {
  queued: "未開始 — この子 issue の pane はまだ起動していません。",
  deferred: "未開始 — open な blocker があるため待機中です。",
  closed: "pane が起動しないまま issue は close されました。",
  unknown: "未開始 — issue 状態を取得できていません。",
};

export function notStartedNote(tmuxState: string): string {
  return NOT_STARTED_NOTES[tmuxState] ?? NOT_STARTED_NOTES["unknown"]!;
}
