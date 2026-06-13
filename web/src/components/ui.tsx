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

/* agentState バッジ。"running" / "done" 以外(空 = pane 死亡・不明)は null を
 * 返し、呼び出し側が省略 or ミュート表示を選ぶ。 */
export function AgentStateTag({ state }: { state?: string }) {
  if (state === "running") return <Tag cls="t-warn">running</Tag>;
  if (state === "done") return <Tag cls="t-ok">done</Tag>;
  return null;
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
