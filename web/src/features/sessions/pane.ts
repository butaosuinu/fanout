import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import type { BlockerStatus, PaneView, PRRef, Snapshot } from "../../transport/types";
import { issueUrl } from "../../shared/github";

/* ghissue.PrimaryPR と同じ選択規則(MERGED 優先、なければ先頭)。ciStatus と
 * 同じ PR を指すよう backend とミラーしておかないと、PR 列と ci 列が別の PR を
 * 表示してしまう。 */
export function prPrimary(prs: PRRef[] | null | undefined): PRRef | null {
  if (!prs || !prs.length) return null;
  return prs.find((p) => p.state === "MERGED") ?? prs[0] ?? null;
}

/* ghissue.PRRef.DisplayState の reviewDecision 部分の語彙。 */
const PR_REVIEW_DISPLAY: Record<string, string> = {
  APPROVED: "approved",
  CHANGES_REQUESTED: "changes-requested",
  REVIEW_REQUIRED: "review-required",
};

/* ghissue.PRRef.DisplayState のミラー。優先順位まで Go と揃えること —
 * merged / closed / draft が reviewDecision より先に決まる。TUI の
 * summarizePRs も同じ語彙を出しているので、ここがズレると TUI と web で
 * 同じ PR が別の状態に見える。 */
export function prDisplayState(pr: PRRef): string {
  const state = (pr.state ?? "").trim().toUpperCase();
  if (state === "MERGED" || pr.mergedAt) return "merged";
  if (state === "CLOSED") return "closed";
  if (pr.isDraft) return "draft";
  return PR_REVIEW_DISPLAY[(pr.reviewDecision ?? "").trim().toUpperCase()] ?? state.toLowerCase();
}

/* ghissue.PRRef.HasConflict のミラー。CONFLICTING だけが「衝突あり」— 欠落は
 * MERGED/CLOSED か GitHub が再計算中で、「衝突なし」の意味ではない。 */
export function prHasConflict(pr: PRRef): boolean {
  return pr.mergeable === "CONFLICTING";
}

/* sessionview.reviewFilterValue のミラー(review: フィルタのフォールバック)。
 * DisplayState と違い merged/draft に潰されない — merge 後も
 * review:approved で引けるようにするため。 */
export function prReviewValue(pr: PRRef | null): string {
  const decision = (pr?.reviewDecision ?? "").trim().toLowerCase();
  return decision ? decision.replaceAll("_", "-") : "none";
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
  if (p.derived?.ci != null) return p.derived.ci;
  if (p.ciStatus == null || p.ciStatus === "") return ciWorst(p.prs);
  return p.ciStatus === "-" ? "" : p.ciStatus;
}

export function paneRuntimeState(p: PaneView): string {
  return p.runtimeState || p.tmuxState;
}

export function paneRuntimeTitle(p: PaneView): string {
  return p.runtimeTitle || p.tmuxTitle || "";
}

/* Legacy recorded panes omit backend because an empty persisted value means
 * tmux. Synthetic not-started rows have no runtime owner, so keep those empty
 * instead of inventing tmux. */
export function paneBackend(p: Pick<PaneView, "backend" | "notStarted">): string {
  const explicit = p.backend?.trim().toLowerCase();
  if (explicit) return explicit;
  return p.notStarted ? "" : "tmux";
}

/* Mirrors Go blockers.FormatStatuses: OPEN → "OPEN #N", CLOSED → "resolved #N",
 * anything else (UNKNOWN etc.) → "<STATE> #N". */
export function blockerLabel(b: BlockerStatus): string {
  if (b.state === "OPEN") return "OPEN";
  if (b.state === "CLOSED") return "resolved";
  return String(b.state ?? "").trim() || "-";
}

export function fmtBlockers(p: PaneView): string {
  if (p.derived?.blockersText) return p.derived.blockersText;
  if (!p.blockers || !p.blockers.length) return "-";
  return p.blockers.map((b) => `${blockerLabel(b)} #${b.num}`).join(", ");
}

export function fmtWave(p: PaneView): string {
  if (p.derived?.waveText && p.derived.waveText !== "-") return p.derived.waveText;
  return p.waveLabel || compactWave(p);
}

export function compactWave(p: PaneView): string {
  return p.wave ? `w${p.wave}` : "";
}

export function openBlockerCount(p: PaneView): number {
  if (p.derived?.openBlockers != null) return p.derived.openBlockers;
  return (p.blockers ?? []).filter((b) => b.state === "OPEN").length;
}

/* blockers セルの "resolved" は全 blocker が CLOSED 確定のときだけ。state 取得に
 * 失敗した UNKNOWN 行が混ざる場合は解決済みと誤認させない。 */
export function blockersAllClosed(p: PaneView): boolean {
  return !!p.blockers && p.blockers.length > 0 && p.blockers.every((b) => b.state === "CLOSED");
}

export function paneLabel(p: PaneView): string {
  if (p.kind === "shell") return "shell";
  if (p.kind === "attached-agent") {
    if (p.sourceTaskId) return p.sourceTaskId;
    if (p.sourceIssueNum && p.sourceIssueNum > 0) return `#${p.sourceIssueNum}`;
    return "attached agent";
  }
  return p.taskId || `#${p.issueNum}`;
}

/* 行の表示名(name 列と同じ値)。derived が正、無ければ記録値から組む。 */
export function paneName(p: PaneView): string {
  return p.derived?.name || p.displayName || p.slug || "";
}

export function paneIssueNum(p: PaneView): number {
  if (p.kind === "attached-agent" && p.sourceIssueNum && p.sourceIssueNum > 0) {
    return p.sourceIssueNum;
  }
  return p.issueNum;
}

export function paneIssueURL(repo: string, p: PaneView): string {
  if (p.taskId || p.sourceTaskId) return "";
  const num = paneIssueNum(p);
  return num > 0 ? issueUrl(repo, num) : "";
}

/* 行の安定キー。tmux 再起動後は pane id (%N) が別 issue の古い行と重複しうる
 * ので、選択は parent + issueNum/taskId で識別し、paneId は capture 対象にだけ使う。
 * plan タスクや @manual のような worktree-local な行は別 worktree 間で
 * (parent,issueNum)/(parent,taskId) が衝突しうるので、sourceKey があれば付けて区別する。 */
export function rowKey(parent: string, p: PaneView): string {
  const suffix = p.sourceKey ? `~${p.sourceKey}` : "";
  if (p.taskId) return `${parent}@${p.taskId}${suffix}`;
  return `${parent}#${p.issueNum}${suffix}`;
}

/* 行キーから pane と所属 session の parent を引く。parent は /api/diff の
 * identity クエリに必要(rowKey から parent を逆パースしない — parent は
 * Projects URL もあり得る自由文字列)。 */
export function findPaneEntry(
  snap: Snapshot | null,
  key: string | null,
): { parent: string; pane: PaneView } | null {
  if (!snap || !key) return null;
  for (const s of snap.sessions ?? []) {
    const parent = String(s.parent ?? "");
    for (const p of s.panes ?? []) if (rowKey(parent, p) === key) return { parent, pane: p };
  }
  return null;
}

/* GET /api/diff の行 identity クエリ(正は docs/local-diff-review-tools.ja.md)。
 * rowKey と同じ識別規則ファミリー — 行種が増えたら両方を揃えること。
 * GitHub issue 行(issueNum>0)は parent+issue、plan task 行は parent+task+source、
 * 負の synthetic issue 行(@manual / attached-agent)は parent+issue+source。
 * identity を組めない行(shell、未開始、worktree 記録なし、source 必須なのに
 * sourceKey 欠落)は null を返し、呼び出し側はボタンを出さない。 */
export function diffQuery(parent: string, p: PaneView): Record<string, string> | null {
  if (p.notStarted || p.kind === "shell" || !p.worktreePath) return null;
  if (p.taskId) {
    return p.sourceKey ? { parent, task: p.taskId, source: p.sourceKey } : null;
  }
  if (p.issueNum > 0) return { parent, issue: String(p.issueNum) };
  if (p.issueNum < 0 && p.sourceKey) {
    return { parent, issue: String(p.issueNum), source: p.sourceKey };
  }
  return null;
}

/* 未開始(synthetic)行の Drawer 状態説明文。キーは tmuxState。モジュール定数は
 * import 時に一度だけ評価されるので、翻訳済み文字列ではなく descriptor を置く。 */
const NOT_STARTED_NOTES: Record<string, MessageDescriptor> = {
  queued: msg`未開始 — この子 issue の pane はまだ起動していません。`,
  deferred: msg`未開始 — open な blocker があるため待機中です。`,
  closed: msg`pane が起動しないまま issue は close されました。`,
  unknown: msg`未開始 — issue 状態を取得できていません。`,
};

export function notStartedNote(tmuxState: string): MessageDescriptor {
  return NOT_STARTED_NOTES[tmuxState] ?? NOT_STARTED_NOTES["unknown"]!;
}
