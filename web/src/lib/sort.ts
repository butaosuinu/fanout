import { paneCI, prPrimary } from "./pane";
import type { PaneView } from "./types";

export type SortDir = 1 | -1;

type SortKeyFn = (p: PaneView) => number | string;

export const SORTS: Record<string, SortKeyFn> = {
  issueNum: (p) => p.derived?.sort?.issueNum ?? p.issueNum ?? 0,
  name: (p) =>
    p.derived?.sort?.name ?? String(p.displayName || p.slug || p.taskId || "").toLowerCase(),
  agent: (p) => p.derived?.sort?.agent ?? String(p.agent ?? "").toLowerCase(),
  wave: (p) => (p.derived?.sort?.wave ?? p.wave) || 99,
  blockers: (p) =>
    p.derived?.sort?.blockers ?? (p.blockers ?? []).filter((b) => b.state === "OPEN").length,
  branch: (p) => p.derived?.sort?.branch ?? String(p.branchName ?? "").toLowerCase(),
  diff: (p) => {
    if (p.derived?.sort?.diff != null) return p.derived.sort.diff;
    const m = /\+(\d+)\/-(\d+)/.exec(p.diffSummary ?? "");
    return m ? +m[1]! + +m[2]! : -1;
  },
  dirty: (p) => p.derived?.sort?.dirty ?? { clean: 0, dirty: 1, unknown: 2 }[p.dirtyState] ?? 3,
  ci: (p) => p.derived?.sort?.ci ?? { fail: 0, pending: 1, pass: 2 }[paneCI(p)] ?? 3,
  tmux: (p) => p.derived?.sort?.tmux ?? (p.alive ? 0 : p.notStarted ? 2 : 1), // alive < stale < 未開始(synthetic)
  state: (p) => p.derived?.sort?.state ?? String(p.issueState ?? "").toLowerCase(),
  pr: (p) => {
    if (p.derived?.sort?.pr != null) return p.derived.sort.pr;
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
