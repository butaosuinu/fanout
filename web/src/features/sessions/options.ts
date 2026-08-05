import type { Snapshot } from "../../transport/types";

/* snapshot から agent / wave の選択肢を導出。agent は重複排除し、トークンが
 * 空白 split される都合上、空白を含む値は除外。wave は数値昇順で
 * value=N(filter の wave case は String(p.wave) と比較)・表示 "wN"。 */

export function deriveAgents(snap: Snapshot | null): string[] {
  const agents = new Set<string>();
  for (const s of snap?.sessions ?? []) {
    for (const p of s.panes ?? []) {
      const a = String(p.agent ?? "").trim();
      if (a && !/\s/.test(a)) agents.add(a);
    }
  }
  return [...agents].sort();
}

export function deriveWaves(snap: Snapshot | null): number[] {
  const waves = new Set<number>();
  for (const s of snap?.sessions ?? []) {
    for (const p of s.panes ?? []) {
      if (p.wave) waves.add(p.wave);
    }
  }
  return [...waves].sort((x, y) => x - y);
}
