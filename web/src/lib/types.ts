/* ワイヤ契約 — Go 側の JSON タグと 1:1 対応(正は internal/sessionview/model.go、
 * internal/ghissue/ghissue.go、internal/blockers/blockers.go、
 * internal/dashboard/peek.go)。タグの綴りに注意: paneId(paneID ではない)、
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
  slug: string;
  displayName: string;
  agent: string;
  branchName: string;
  paneId: string;
  worktreePath: string;
  createdAt: string;
  alive: boolean;
  issueState: string; // "OPEN" / "CLOSED" / "UNKNOWN"
  prs: PRRef[] | null;
  hasMergedPr: boolean;
  diffSummary: string; // "+X/-Y" or free text
  dirtyState: string; // "dirty" / "clean" / "unknown"
  worktreeErr?: string;
  tmuxState: string; // "live" / "stale" / "unknown" / "-"
  tmuxTitle?: string;
  agentState?: string; // "running" / "done" / ""(不明)
  prompt?: string;
  ciStatus?: string; // lowercase; "-" = primary PR に CI なし
  wave?: number; // 0(unknown)はフィールドごと欠落
  waveLabel?: string;
  blockers: BlockerStatus[];
  blocked: boolean;
}

export interface Rollup {
  total: number;
  merged: number;
  pending: number;
  live: number;
  running: number;
  blocked: number;
  allMerged: boolean;
}

export interface Degraded {
  tmux: boolean;
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
