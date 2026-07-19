import {
  compactWave,
  fmtBlockers,
  fmtWave,
  paneBackend,
  paneCI,
  paneRuntimeState,
  paneRuntimeTitle,
  prPrimary,
} from "./pane";
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
  "task",
  "pr",
  "run",
  "backend",
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
  const hay =
    p.derived?.filterText ||
    [
      p.issueNum,
      p.taskId,
      p.displayName,
      p.slug,
      p.agent,
      p.branchName,
      p.diffSummary,
      p.dirtyState,
      p.issueState,
      p.paneId,
      paneBackend(p),
      paneRuntimeTitle(p),
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
      case "state": {
        const runtimeState = paneRuntimeState(p);
        // runtime 状態に一致するならそれを、さもなくば issue 状態を見る。
        if (
          (runtimeState === t.value ? runtimeState : String(p.issueState ?? "").toLowerCase()) !==
          t.value
        )
          return false;
        break;
      }
      case "run":
        if ((p.derived?.filterValues?.run ?? p.agentState ?? "") !== t.value) return false;
        break;
      case "agent":
        if (
          !String(p.agent ?? "")
            .toLowerCase()
            .includes(t.value)
        )
          return false;
        break;
      case "backend":
        if ((p.derived?.filterValues?.backend ?? paneBackend(p)).toLowerCase() !== t.value)
          return false;
        break;
      case "wave":
        if (
          String(p.wave ?? "") !== t.value &&
          (p.waveLabel ?? "").toLowerCase() !== t.value &&
          (p.derived?.dependencyWave ?? "").toLowerCase() !== t.value &&
          (p.derived?.waveText ?? "").toLowerCase() !== t.value &&
          compactWave(p).toLowerCase() !== t.value &&
          fmtWave(p).toLowerCase() !== t.value
        )
          return false;
        break;
      case "ci":
        if (paneCI(p) !== t.value) return false;
        break;
      case "dirty":
        if (
          (p.derived?.filterValues?.dirty ?? (p.dirtyState === "dirty" ? "yes" : "no")) !== t.value
        )
          return false;
        break;
      case "live":
        if ((p.derived?.filterValues?.live ?? (p.alive ? "yes" : "no")) !== t.value) return false;
        break;
      case "issue":
        if (
          (p.derived?.filterValues?.issue ?? String(p.issueNum)) !== t.value &&
          String(p.issueNum) !== t.value &&
          String(p.taskId ?? "").toLowerCase() !== t.value
        )
          return false;
        break;
      case "task":
        if ((p.taskId ?? "").toLowerCase() !== t.value) return false;
        break;
      case "pr":
        if ((p.derived?.filterValues?.pr ?? pr?.state ?? "none").toLowerCase() !== t.value)
          return false;
        break;
    }
  }
  return true;
}

/* ---- フィルタ文字列のトークン操作(ドロップダウン+チップ UI 用) ---- */

export function filterTokens(filter: string): string[] {
  return filter.trim().split(/\s+/).filter(Boolean);
}

/* key: プレフィックス一致(大文字小文字無視)。replaceToken / stripKey /
 * tokenForKey は必ずこの 1 つの判定を共有する(判定が割れると trigger の
 * 点灯・チェック表示・チップ行が食い違う)。 */
function hasKey(tok: string, key: string): boolean {
  return tok.toLowerCase().startsWith(`${key}:`);
}

/* 同キーの既存トークンを置き換えて追加(state:open → state:closed は上書き) */
export function replaceToken(tokens: string[], key: string, value: string): string[] {
  const toks = stripKey(tokens, key);
  toks.push(`${key}:${value}`);
  return toks;
}

/* 指定キーのトークンを全て除去(手打ちの重複・大文字違いも残さない)。
 * ドロップダウンのトグルオフ用 — exact-match の removeToken だと
 * 「state:open STATE:CLOSED」のような手打ち重複の取り残しが出る。 */
export function stripKey(tokens: string[], key: string): string[] {
  return tokens.filter((t) => !hasKey(t, key));
}

export function removeToken(tokens: string[], tok: string): string[] {
  return tokens.filter((t) => t !== tok);
}

/* 指定キーの最初の key:value トークンの値(小文字)。ドロップダウンの
 * アクティブ表示・トグルオフ判定用。該当キーが無ければ null。 */
export function tokenForKey(tokens: string[], key: string): string | null {
  for (const t of tokens) {
    if (hasKey(t, key)) return t.slice(key.length + 1).toLowerCase();
  }
  return null;
}
