import {
  compactWave,
  fmtBlockers,
  fmtWave,
  paneBackend,
  paneCI,
  paneRuntimeState,
  paneRuntimeTitle,
  prPrimary,
  prReviewValue,
} from "../sessions/pane";
import type { PaneView } from "../../transport/types";

/* 構造化フィルタ: key:value + 自由語、すべて AND。未知キーは自由語に降格。
 * #filter のテキストが単一の真実 — ドロップダウンは key:value トークンを書き
 * 込むだけで手打ち構文と完全互換。 */

/* 比較の共通正規化。key:value の value は parseQuery で小文字化済みなので、
 * 突き合わせる側も同じ規則に揃える。 */
function lower(v: string | number | null | undefined): string {
  return String(v ?? "").toLowerCase();
}

function yesNo(on: boolean): string {
  return on ? "yes" : "no";
}

/* バックエンドが算出済みのフィルタ値。ある値はそれが正で、無いキーだけ記録値から
 * 組み直す(古い snapshot でも手打ち構文が壊れないように)。 */
function derivedValues(p: PaneView): Record<string, string> {
  return p.derived?.filterValues ?? {};
}

/* wave: が受け付ける別名 — 数値・waveLabel・derived の 2 種・wN 表記。
 * どれか 1 つに完全一致すれば通す。 */
function waveAliases(p: PaneView): string[] {
  return [
    p.wave,
    p.waveLabel,
    p.derived?.dependencyWave,
    p.derived?.waveText,
    compactWave(p),
    fmtWave(p),
  ].map(lower);
}

type FilterPredicate = (p: PaneView, value: string) => boolean;

/* キーごとの述語テーブル。キーのセマンティクス(完全一致か部分一致か、derived の
 * 値を優先するか)はここ 1 か所だけを読めば分かるようにしてある。 */
const FILTER_PREDICATES = new Map<string, FilterPredicate>([
  // runtime 状態に一致するならそれを、さもなくば issue 状態を見る。
  ["state", (p, v) => paneRuntimeState(p) === v || lower(p.issueState) === v],
  ["run", (p, v) => (derivedValues(p).run ?? p.agentState ?? "") === v],
  ["agent", (p, v) => lower(p.agent).includes(v)],
  ["backend", (p, v) => lower(derivedValues(p).backend ?? paneBackend(p)) === v],
  ["wave", (p, v) => waveAliases(p).includes(v)],
  ["ci", (p, v) => paneCI(p) === v],
  ["dirty", (p, v) => (derivedValues(p).dirty ?? yesNo(p.dirtyState === "dirty")) === v],
  ["live", (p, v) => (derivedValues(p).live ?? yesNo(p.alive)) === v],
  [
    "issue",
    (p, v) =>
      (derivedValues(p).issue ?? String(p.issueNum)) === v ||
      String(p.issueNum) === v ||
      lower(p.taskId) === v,
  ],
  ["task", (p, v) => lower(p.taskId) === v],
  ["pr", (p, v) => lower(derivedValues(p).pr ?? prPrimary(p.prs)?.state ?? "none") === v],
  // pr: がライフサイクル状態(open/closed/merged)なのに対し、review: はレビュー状態。
  // 直交する軸なので、merged の行も review:approved で引ける。
  ["review", (p, v) => lower(derivedValues(p).review ?? prReviewValue(prPrimary(p.prs))) === v],
]);

/* パースが key:value として受け付けるキーは述語テーブルそのもの。片方にだけ
 * キーを足すと「key:value が自由語に落ちる」「制約なしで素通りする」の食い違いが
 * 出るので、2 つのリストを持たない。 */
export const FILTER_KEYS = new Set(FILTER_PREDICATES.keys());

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

/* 自由語の検索対象。derived.filterText があればそれが正。 */
function haystack(p: PaneView): string {
  return (
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
      .toLowerCase()
  );
}

/* 全 term の AND。述語を持たないキー(手組みの Term)は制約なしとして通す。 */
export function matches(p: PaneView, terms: Term[]): boolean {
  const hay = haystack(p);
  return terms.every((t) =>
    t.kind === "word" ? hay.includes(t.word) : (FILTER_PREDICATES.get(t.key)?.(p, t.value) ?? true),
  );
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
