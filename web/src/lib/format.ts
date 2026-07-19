import type { Degraded } from "./types";

export function clock(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "--:--:--" : d.toTimeString().slice(0, 8);
}

export function parseDiff(diffSummary: string): { add: string; del: string } | null {
  const m = /^\+(\d+)\/-(\d+)$/.exec(diffSummary ?? "");
  return m ? { add: m[1]!, del: m[2]! } : null;
}

export function fmtCreated(createdAt: string): string {
  return createdAt ? createdAt.replace("T", " ").slice(0, 16) : "—";
}

export function degradedMessages(deg: Degraded | null | undefined): string[] {
  const msgs: string[] = [];
  if (!deg) return msgs;
  if (deg.github) msgs.push("GitHub データ取得が不安定 — issue / PR / CI 列は劣化表示");
  if (deg.runtime) {
    msgs.push("runtime の一部が利用できません — ペイン生死・peek / plan は劣化表示");
  } else if (deg.tmux) {
    // Older snapshots expose only the tmux compatibility field, but the
    // user-facing failure surface is runtime-neutral.
    msgs.push("runtime が利用できません — ペイン生死・peek は劣化表示");
  }
  // A state-load failure sets only degraded.reason (no source flag); surface it
  // so a corrupted .fanout/state.json shows a warning, not a silent empty view.
  if (deg.reason && !deg.github && !deg.runtime && !deg.tmux) {
    msgs.push(`state 読み込みに失敗: ${deg.reason}`);
  }
  return msgs;
}
