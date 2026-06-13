import { paneCI, prPrimary } from "./pane";
import type { PaneView } from "./types";

export type SortDir = 1 | -1;

type SortKeyFn = (p: PaneView) => number | string;

export const SORTS: Record<string, SortKeyFn> = {
  issueNum: (p) => p.issueNum ?? 0,
  name: (p) => String(p.displayName || p.slug || p.taskId || "").toLowerCase(),
  agent: (p) => String(p.agent ?? "").toLowerCase(),
  wave: (p) => p.wave || 99,
  blockers: (p) => (p.blockers ?? []).filter((b) => b.state === "OPEN").length,
  branch: (p) => String(p.branchName ?? "").toLowerCase(),
  diff: (p) => {
    const m = /\+(\d+)\/-(\d+)/.exec(p.diffSummary ?? "");
    return m ? +m[1]! + +m[2]! : -1;
  },
  dirty: (p) => ({ clean: 0, dirty: 1, unknown: 2 })[p.dirtyState] ?? 3,
  ci: (p) => ({ fail: 0, pending: 1, pass: 2 })[paneCI(p)] ?? 3,
  tmux: (p) => (p.alive ? 0 : p.notStarted ? 2 : 1), // alive < stale < 未開始(synthetic)
  state: (p) => String(p.issueState ?? "").toLowerCase(),
  pr: (p) => {
    const pr = prPrimary(p.prs);
    return { MERGED: 0, OPEN: 1, CLOSED: 2 }[pr?.state ?? ""] ?? 3;
  },
};

export const COLS: ReadonlyArray<readonly [key: string, label: string]> = [
  ["issueNum", "issue"],
  ["name", "name"],
  ["agent", "agent"],
  ["wave", "wave"],
  ["blockers", "blockers"],
  ["branch", "branch"],
  ["diff", "diff"],
  ["dirty", "dirty"],
  ["ci", "ci"],
  ["tmux", "tmux"],
  ["state", "state"],
  ["pr", "pr"],
];

function tieKey(p: PaneView): string {
  return String(p.taskId || p.issueNum).toLowerCase();
}

function tieCompare(a: PaneView, b: PaneView): number {
  if ((a.issueNum ?? 0) > 0 && (b.issueNum ?? 0) > 0) {
    return (a.issueNum ?? 0) - (b.issueNum ?? 0);
  }
  return tieKey(a).localeCompare(tieKey(b));
}

/* 非破壊ソート。一次キーが同値なら issueNum/taskId 昇順の安定二次ソート。 */
export function sortPanes(panes: PaneView[], sortKey: string, dir: SortDir): PaneView[] {
  const fn = SORTS[sortKey] ?? SORTS["issueNum"]!;
  return [...panes].sort((a, b) => {
    const ka = fn(a);
    const kb = fn(b);
    if (ka < kb) return -dir;
    if (ka > kb) return dir;
    return tieCompare(a, b);
  });
}
