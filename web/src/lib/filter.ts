import { fmtBlockers, fmtWave, paneCI, prPrimary } from "./pane";
import type { PaneView } from "./types";

/* 構造化フィルタ: key:value + 自由語、すべて AND。未知キーは自由語に降格。
 * #filter のテキストが単一の真実 — ドロップダウンは key:value トークンを書き
 * 込むだけで手打ち構文と完全互換。 */

export const FILTER_KEYS = new Set([
  "state",
  "agent",
  "wave",
  "ci",
  "dirty",
  "live",
  "issue",
  "pr",
  "run",
]);

export type Term = { kind: "key"; key: string; value: string } | { kind: "word"; word: string };

export function parseQuery(str: string): Term[] {
  const terms: Term[] = [];
  for (const tok of String(str).trim().split(/\s+/)) {
    if (!tok) continue;
    const m = /^([a-z]+):(\S*)$/i.exec(tok);
    if (m && FILTER_KEYS.has(m[1]!.toLowerCase())) {
      terms.push({ kind: "key", key: m[1]!.toLowerCase(), value: m[2]!.toLowerCase() });
    } else {
      terms.push({ kind: "word", word: tok.toLowerCase() });
    }
  }
  return terms;
}

export function matches(p: PaneView, terms: Term[]): boolean {
  const hay = [
    p.issueNum,
    p.displayName,
    p.slug,
    p.agent,
    p.branchName,
    p.diffSummary,
    p.dirtyState,
    p.issueState,
    p.tmuxTitle,
    p.agentState,
    fmtWave(p),
    fmtBlockers(p),
  ]
    .join(" ")
    .toLowerCase();
  for (const t of terms) {
    if (t.kind === "word") {
      if (!hay.includes(t.word)) return false;
      continue;
    }
    const pr = prPrimary(p.prs);
    switch (t.key) {
      case "state":
        // tmux 状態(live/stale)に一致するならそれを、さもなくば issue 状態を見る
        if ((p.tmuxState === t.value ? p.tmuxState : String(p.issueState ?? "").toLowerCase()) !== t.value) return false;
        break;
      case "run":
        if ((p.agentState ?? "") !== t.value) return false;
        break;
      case "agent":
        if (!String(p.agent ?? "").toLowerCase().includes(t.value)) return false;
        break;
      case "wave":
        if (
          String(p.wave ?? "") !== t.value &&
          (p.waveLabel ?? "").toLowerCase() !== t.value &&
          fmtWave(p).toLowerCase() !== t.value
        )
          return false;
        break;
      case "ci":
        if (paneCI(p) !== t.value) return false;
        break;
      case "dirty":
        if ((t.value === "yes") !== (p.dirtyState === "dirty")) return false;
        break;
      case "live":
        if ((t.value === "yes") !== !!p.alive) return false;
        break;
      case "issue":
        if (String(p.issueNum) !== t.value) return false;
        break;
      case "pr":
        if ((pr?.state ?? "none").toLowerCase() !== t.value) return false;
        break;
    }
  }
  return true;
}

/* ---- フィルタ文字列のトークン操作(ドロップダウン+チップ UI 用) ---- */

export function filterTokens(filter: string): string[] {
  return filter.trim().split(/\s+/).filter(Boolean);
}

/* 同キーの既存トークンを置き換えて追加(state:open → state:closed は上書き) */
export function replaceToken(tokens: string[], key: string, value: string): string[] {
  const prefix = `${key}:`;
  const toks = tokens.filter((t) => !t.toLowerCase().startsWith(prefix));
  toks.push(prefix + value);
  return toks;
}

export function removeToken(tokens: string[], tok: string): string[] {
  return tokens.filter((t) => t !== tok);
}

/* 指定キーの key:value トークンを探す(手打ち含め最初の 1 つ)。raw は
 * removeToken での exact-match 除去用の原文、value は選択肢との小文字比較用。 */
export function tokenForKey(tokens: string[], key: string): { raw: string; value: string } | null {
  const prefix = `${key}:`;
  for (const t of tokens) {
    if (t.toLowerCase().startsWith(prefix)) return { raw: t, value: t.slice(prefix.length).toLowerCase() };
  }
  return null;
}
