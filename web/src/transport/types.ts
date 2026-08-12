/* ワイヤ契約 — Go 側の JSON タグと 1:1 対応(正は internal/app/sessionview/model.go、
 * internal/infra/ghissue/ghissue.go、internal/core/blockers/blockers.go、
 * internal/ui/dashboard/peek.go)。タグの綴りに注意: paneId(paneID ではない)、
 * hasMergedPr(Pr が小文字)、PRRef の ci(ciStatus ではない)。Go の nil slice
 * は null で届くので sessions / panes / prs は null 許容(blockers のみ常に []
 * が保証されている)。omitempty のフィールドは optional。 */

export interface Snapshot {
  repo: string; // "owner/name"; 未解決時 ""
  projectRoot: string;
  generatedAt: string; // RFC3339
  sessions: Session[] | null;
  rollup: Rollup;
  degraded: Degraded;
}

export interface Session {
  parent: string; // issue 番号文字列 or Projects v2 URL
  panes: PaneView[] | null;
  rollup: Rollup;
}

export interface PaneView {
  issueNum: number;
  taskId?: string;
  kind?: string;
  slug: string;
  displayName: string;
  agent: string;
  backend?: string; // "tmux" / "herdr"; legacy snapshot は欠落 = tmux
  branchName: string;
  baseBranch: string;
  paneId: string;
  shellKey?: string;
  sourceIssueNum?: number;
  sourceTaskId?: string;
  worktreePath: string;
  /* worktree-local な行(plan タスク・@manual)を識別する安定トークン。別 worktree の
   * 同一 (parent,issueNum)/(parent,taskId) 行が行キーで衝突しないよう rowKey に混ぜる。
   * GitHub issue 行(issueNum>0)では欠落。 */
  sourceKey?: string;
  createdAt: string;
  alive: boolean;
  issueState: string; // "OPEN" / "CLOSED" / "UNKNOWN"
  prs: PRRef[] | null;
  hasMergedPr: boolean;
  diffSummary: string; // "+X/-Y" or free text
  dirtyState: string; // "dirty" / "clean" / "unknown"
  worktreeErr?: string;
  tmuxState: string; // compatibility field: "live" / "stale" / "unknown" / "-"
  tmuxTitle?: string;
  runtimeState?: string; // backend-neutral field; additive "unsupported" を含む
  runtimeTitle?: string; // backend-neutral alias; tmuxTitle は互換用に残る
  agentState?: string; // "running" / "working" / "plan" / "blocked" / "idle" / "done" / ""(不明)
  planMode?: boolean; // plan mode 起動ペイン(全 agent 共通。codex のみ /api/plan・Plan セクション対象)
  prompt?: string;
  ciStatus?: string; // lowercase; "-" = primary PR に CI なし
  wave?: number; // 0(unknown)はフィールドごと欠落
  waveLabel?: string;
  blockers: BlockerStatus[];
  blocked: boolean;
  /* 記録 pane の無い「未開始」子 issue の synthetic 行。pane 由来フィールドは
   * zero、tmuxState は closed / deferred / queued / unknown(TUI と同一文字列) */
  notStarted?: boolean;
  derived?: PaneDerived;
}

export interface PaneDerived {
  name?: string;
  prSummary?: string;
  primaryPrNumber?: number;
  primaryPrState?: string;
  ci?: string;
  waveBadge?: string;
  waveText?: string;
  dependencyWave?: string;
  blockersText?: string;
  openBlockers?: number;
  diffTotal?: number;
  diffParsed?: boolean;
  filterText?: string;
  filterValues?: Record<string, string>;
  sort?: PaneSortKeys;
  canFocus?: boolean;
  canPeek?: boolean;
  worktreeRelative?: string;
}

export interface PaneSortKeys {
  issueNum?: number;
  name?: string;
  agent?: string;
  wave?: number;
  blockers?: number;
  branch?: string;
  diff?: number;
  dirty?: number;
  ci?: number;
  tmux?: number;
  state?: string;
  pr?: number;
}

export interface Rollup {
  total: number; // synthetic(未開始)行込み
  merged: number;
  pending: number;
  live: number;
  running: number;
  blocked: number;
  notStarted?: number; // 未開始子 issue(synthetic 行)数。旧サーバーの snapshot は省略
  allMerged: boolean;
}

export interface Degraded {
  tmux: boolean;
  runtime?: boolean;
  github: boolean;
  reason?: string;
}

export interface PRRef {
  number: number;
  state: string; // "OPEN" / "CLOSED" / "MERGED"
  mergedAt: string | null;
  isDraft?: boolean;
  reviewDecision?: string;
  ci?: string;
  /* GitHub の MergeableState から UNKNOWN を落とした値: "CONFLICTING" /
   * "MERGEABLE" / 欠落。MERGED・CLOSED の PR は常に欠落し、GitHub が base push
   * 後に再計算する間も欠落するので、欠落は「不明」であって「衝突なし」ではない。 */
  mergeable?: string;
  /* PullRequest.totalCommentsCount(会話コメント + inline レビューコメント)。
   * 0 件は omitempty で欠落する。 */
  comments?: number;
}

export interface BlockerStatus {
  num: number;
  state: string;
}

export interface PeekResponse {
  paneId: string;
  lines: number;
  capturedAt: string;
  output: string;
}

/* 正は internal/ui/dashboard/plan.go の planResponse。found:false は「codex の
 * plan-mode ペインだが取得可能な出力に完全な plan ブロックが無い」を表す正常応答(200)。 */
export interface PlanResponse {
  paneId: string;
  capturedAt: string;
  found: boolean;
  plan: string;
}

/* GET /api/diff — 正は docs/local-diff-review-tools.ja.md の wire contract(#576)。
 * サーバー実装は #577/#578。files は 500 files 上限内の全件(空でも [])、patch は
 * `diff --git` 始まりの file block の連結。エラー body は {"error":"message"}。 */
export type DiffOmittedReason = "" | "binary" | "tooLarge" | "collectionLimit" | "responseLimit";

export interface DiffFileEntry {
  path: string;
  oldPath?: string; // rename の merge-base 側パス。rename でない file では欠落
  additions: number | null; // collectionLimit で省略された file は null
  deletions: number | null;
  binary: boolean;
  patchIncluded: boolean;
  omittedReason: DiffOmittedReason;
}

export interface DiffResponse {
  paneId: string;
  branchName: string;
  baseBranch: string;
  mergeBase: string; // strict 解決済み commit SHA
  capturedAt: string; // RFC3339 UTC
  files: DiffFileEntry[];
  patch: string;
  truncated: boolean;
  totalBytes: number;
}
