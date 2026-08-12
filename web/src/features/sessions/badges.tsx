import { useLingui } from "@lingui/react/macro";
import { prUrl } from "../../shared/github";
import type { PRRef } from "../../transport/types";
import { GhLink, Tag } from "../../ui/Tag";
import { prDisplayState, prHasConflict } from "./pane";

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

/* prDisplayState の語彙ごとのタグ色。approved は緑、changes-requested は赤、
 * review-required と open は浅葱、draft は破線、merged は藍。closed は既存どおり
 * クラス無し(素の枠)。 */
const PR_STATE_CLASSES: Record<string, string> = {
  merged: "t-merged",
  open: "t-open",
  draft: "t-open t-draft",
  approved: "t-ok",
  "changes-requested": "t-err",
  "review-required": "t-open",
};

/* 行の PR 列・ドロワーの PR リスト共通: ピルごと PR ページへのリンクに包む。
 * PRRef carries no title on the wire (ghissue.PRRef) — number/state only.
 * ラベルは TUI(summarizePRs)と同じ DisplayState 語彙。 */
export function PrPill({ repo, pr }: { repo: string; pr: PRRef | null }) {
  if (!pr) return <span className="muted">—</span>;
  const label = prDisplayState(pr) || pr.state;
  return (
    <GhLink url={prUrl(repo, pr.number)} cls="gh gh-pill">
      <Tag cls={PR_STATE_CLASSES[label] ?? ""}>{`#${pr.number} ${label}`}</Tag>
    </GhLink>
  );
}

/* base と競合している PR にだけ出す。mergeable 欠落(MERGED/CLOSED、GitHub の
 * 再計算中)は「不明」なので何も出さない。 */
export function PrConflictTag({ pr }: { pr: PRRef }) {
  const { t } = useLingui();
  if (!prHasConflict(pr)) return null;
  return (
    <Tag cls="t-err" title={t`base branch と競合しています`}>
      conflict
    </Tag>
  );
}

/* PR のコメント件数。0 件はタグごと出さない — 全行に「0」が並ぶとノイズになる。 */
export function PrCommentsTag({ pr }: { pr: PRRef }) {
  const { t } = useLingui();
  const count = pr.comments ?? 0;
  if (count <= 0) return null;
  return (
    <Tag title={t`コメント ${{ count }} 件(inline レビューコメントを含む)`}>{`💬 ${count}`}</Tag>
  );
}

/* PR 単位の CI タグ(ドロワー用)。行の CiCell と語彙は同じで接頭辞だけ違う。 */
export function PrCiTag({ ci }: { ci?: string }) {
  if (ci === "pass") return <Tag cls="t-ok">ci pass</Tag>;
  if (ci === "fail") return <Tag cls="t-err">ci fail</Tag>;
  if (ci) return <Tag cls="t-warn">ci pending</Tag>;
  return null;
}

/* 生の reviewDecision タグ(ドロワー用)。行のピルは DisplayState 語彙に潰すので、
 * merged 済み PR の approved はここでしか見えない。 */
export function PrReviewTag({ decision }: { decision?: string }) {
  if (!decision) return null;
  return <Tag>{decision.toLowerCase().replaceAll("_", " ")}</Tag>;
}
