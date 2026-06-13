import type { PaneView, Rollup, Session, Snapshot } from "../lib/types";

export function makeRollup(over: Partial<Rollup> = {}): Rollup {
  return {
    total: 0,
    merged: 0,
    pending: 0,
    live: 0,
    running: 0,
    blocked: 0,
    notStarted: 0,
    allMerged: false,
    ...over,
  };
}

export function makePane(over: Partial<PaneView> = {}): PaneView {
  return {
    issueNum: 101,
    slug: "fix-thing",
    displayName: "Fix thing",
    agent: "claude",
    branchName: "fanout/fix-thing",
    paneId: "%1",
    worktreePath: "/tmp/wt/fix-thing",
    createdAt: "2026-06-13T01:23:45Z",
    alive: true,
    issueState: "OPEN",
    prs: null,
    hasMergedPr: false,
    diffSummary: "+10/-2",
    dirtyState: "clean",
    tmuxState: "live",
    blockers: [],
    blocked: false,
    ...over,
  };
}

/* 未開始(synthetic)子 issue 行 — Go 側 Build が emit する synthetic PaneView
 * と同じ形(pane 由来フィールドは zero、diff/dirty は "-")。 */
export function makeQueuedPane(over: Partial<PaneView> = {}): PaneView {
  return {
    issueNum: 103,
    slug: "",
    displayName: "Queued child",
    agent: "",
    branchName: "",
    paneId: "",
    worktreePath: "",
    createdAt: "",
    alive: false,
    issueState: "OPEN",
    prs: null,
    hasMergedPr: false,
    diffSummary: "-",
    dirtyState: "-",
    tmuxState: "queued",
    ciStatus: "-", // Go 側は synthetic 行にも常に CIStatus を入れる
    blockers: [],
    blocked: false,
    notStarted: true,
    ...over,
  };
}

export function makeSession(
  parent: string,
  panes: PaneView[],
  rollup: Partial<Rollup> = {},
): Session {
  return { parent, panes, rollup: makeRollup({ total: panes.length, ...rollup }) };
}

export function makeSnapshot(sessions: Session[], over: Partial<Snapshot> = {}): Snapshot {
  const total = sessions.reduce((n, s) => n + (s.panes?.length ?? 0), 0);
  return {
    repo: "octo/fanout",
    projectRoot: "/tmp/repo",
    generatedAt: "2026-06-13T01:23:45Z",
    sessions,
    rollup: makeRollup({ total }),
    degraded: { tmux: false, github: false },
    ...over,
  };
}
