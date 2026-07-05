import type { ReactNode } from "react";
import { prUrl } from "../lib/github";
import type { PRRef } from "../lib/types";

export function Tag({
  cls = "",
  title,
  children,
}: {
  cls?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span className={cls ? `tag ${cls}` : "tag"} title={title}>
      {children}
    </span>
  );
}

/* url が空(repo 未解決・@manual の負番号 issue など)ならリンク化せず子を
 * そのまま返す。url の安全性は lib/github の検証で担保済み — ここでは新しい
 * URL を組み立てないこと。 */
export function GhLink({
  url,
  cls = "gh",
  children,
}: {
  url: string;
  cls?: string;
  children: ReactNode;
}) {
  if (!url) return <>{children}</>;
  return (
    <a className={cls} href={url} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}

/* agentState の 6 値契約(sessionview normalizeAgentState の許可リスト)ごとの
 * タグ色。進行中(running / working)はアンバー、plan は浅葱、blocked は赤、
 * idle は破線、done は緑。既存 .tag バリアントの再利用で CSS 追加なし。 */
const AGENT_STATE_CLASSES = {
  running: "t-warn",
  working: "t-warn",
  plan: "t-open",
  blocked: "t-err",
  idle: "t-draft",
  done: "t-ok",
} as const;

/* 契約 6 値の並び(挿入順)。FilterBar の run ドロップダウンが順序ごと使う。 */
export const AGENT_STATES = Object.keys(AGENT_STATE_CLASSES) as readonly string[];

/* state が 6 値契約の値かどうか。行・ドロワーの表示ゲートが AgentStateTag と
 * 同じ語彙で判定するための共有ヘルパー。Object.hasOwn で prototype 由来の
 * キー("toString" 等)を弾く。 */
export function isKnownAgentState(state?: string): state is keyof typeof AGENT_STATE_CLASSES {
  return !!state && Object.hasOwn(AGENT_STATE_CLASSES, state);
}

/* agentState バッジ。契約 6 値以外(空 = pane 死亡・不明)は null を返し、
 * 呼び出し側が省略 or ミュート表示を選ぶ。 */
export function AgentStateTag({ state }: { state?: string }) {
  if (!isKnownAgentState(state)) return null;
  return <Tag cls={AGENT_STATE_CLASSES[state]}>{state}</Tag>;
}

/* dirty 状態のタグ。unknown 時の表示は行("—")とドロワー("unknown")で
 * 異なるため引数化する。 */
export function DirtyTag({ state, unknownLabel = "—" }: { state: string; unknownLabel?: string }) {
  if (state === "clean") return <Tag cls="t-ok">clean</Tag>;
  if (state === "dirty") return <Tag cls="t-warn">dirty</Tag>;
  return <span className="muted">{unknownLabel}</span>;
}

/* issue 状態のタグ。unknown 時の表示は行("?")とドロワー("UNKNOWN")で
 * 異なるため引数化する。 */
export function IssueStateTag({
  state,
  unknownLabel = "?",
}: {
  state: string;
  unknownLabel?: string;
}) {
  if (state === "OPEN") return <Tag cls="t-open">OPEN</Tag>;
  if (state === "CLOSED") return <Tag>CLOSED</Tag>;
  return <span className="muted">{unknownLabel}</span>;
}

/* 行の PR 列・ドロワーの PR リスト共通: ピルごと PR ページへのリンクに包む。
 * PRRef carries no title on the wire (ghissue.PRRef) — number/state only. */
export function PrPill({ repo, pr }: { repo: string; pr: PRRef | null }) {
  if (!pr) return <span className="muted">—</span>;
  const cls =
    (pr.state === "MERGED" ? "t-merged" : pr.state === "OPEN" ? "t-open" : "") +
    (pr.isDraft ? " t-draft" : "");
  return (
    <GhLink url={prUrl(repo, pr.number)} cls="gh gh-pill">
      <Tag cls={cls.trim()}>{`#${pr.number} ${pr.isDraft ? "draft" : pr.state}`}</Tag>
    </GhLink>
  );
}
