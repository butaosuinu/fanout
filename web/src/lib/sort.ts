import { paneCI, prPrimary } from "./pane";
import type { PaneView } from "./types";

export type SortDir = 1 | -1;

type SortKeyFn = (p: PaneView) => number | string;

export const SORTS: Record<string, SortKeyFn> = {
  issueNum: (p) => p.issueNum ?? 0,
  name: (p) => String(p.displayName || p.slug || "").toLowerCase(),
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
  tmux: (p) => (p.alive ? 0 : 1),
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

/* 非破壊ソート。一次キーが同値なら issueNum 昇順の安定二次ソート。 */
export function sortPanes(panes: PaneView[], sortKey: string, dir: SortDir): PaneView[] {
  const fn = SORTS[sortKey] ?? SORTS["issueNum"]!;
  return [...panes].sort((a, b) => {
    const ka = fn(a);
    const kb = fn(b);
    if (ka < kb) return -dir;
    if (ka > kb) return dir;
    return (a.issueNum ?? 0) - (b.issueNum ?? 0);
  });
}
