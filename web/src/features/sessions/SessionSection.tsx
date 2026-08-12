import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import type { KeyboardEvent, MouseEvent } from "react";
import { parentLabel, parentUrl } from "../../shared/github";
import { parseDiff } from "../../shared/format";
import {
  blockersAllClosed,
  diffQuery,
  fmtBlockers,
  fmtWave,
  openBlockerCount,
  paneBackend,
  paneCI,
  paneIssueURL,
  paneLabel,
  paneName,
  paneRuntimeState,
  paneRuntimeTitle,
  prPrimary,
  rowKey,
} from "./pane";
import { COLS, type SortDir } from "./sort";
import type { PaneView, PRRef, Rollup } from "../../transport/types";
import {
  AgentStateTag,
  DirtyTag,
  isKnownAgentState,
  IssueStateTag,
  PrCommentsTag,
  PrConflictTag,
  PrPill,
} from "./badges";
import { GhLink, Tag } from "../../ui/Tag";

function BlockersCell({ pane }: { pane: PaneView }) {
  if (pane.blocked) return <Tag cls="t-warn">{`${openBlockerCount(pane)} open`}</Tag>;
  if (!pane.blockers || !pane.blockers.length) return <span className="muted">—</span>;
  return <span className="muted">{blockersAllClosed(pane) ? "resolved" : "unknown"}</span>;
}

/* paneCI は snapshot 由来の自由文字列。既知の 3 値だけタグにして、それ以外
 * (CI 無し・未知の値)は — にする。 */
function CiCell({ ci }: { ci: string }) {
  if (ci === "pass") return <Tag cls="t-ok">pass</Tag>;
  if (ci === "fail") return <Tag cls="t-err">fail</Tag>;
  if (ci === "pending") return <Tag cls="t-warn">pending</Tag>;
  return <span className="muted">—</span>;
}

/* pr 列は primary PR 1 件ぶんの信号をまとめる: 状態ピル + conflict + コメント件数。
 * 各タグは該当しなければ null を返すので、ここに条件分岐は要らない。タグはリンクの
 * 外に置く — 中に入れるとリンクの accessible name に混ざる。 */
function PrCell({ repo, pr }: { repo: string; pr: PRRef | null }) {
  if (!pr) return <span className="muted">—</span>;
  return (
    <span className="pr-cell">
      <PrPill repo={repo} pr={pr} />
      <PrConflictTag pr={pr} />
      <PrCommentsTag pr={pr} />
    </span>
  );
}

/* diff 導線の accessible name。行を指す情報を重複なく並べ、最後に統計を置く。
 * <Trans> ではなく descriptor — <Trans> は {変数} 前後の空白を落とすため、
 * 区切りの空白に意味があるこの名前が壊れる。 */
function diffLinkLabel(pane: PaneView, d: { add: string; del: string } | null): MessageDescriptor {
  const parts = [paneLabel(pane), paneName(pane), pane.branchName ?? ""];
  const target = parts.filter((s, i) => s && parts.indexOf(s) === i).join(" ");
  const stat = d ? `+${d.add}/-${d.del}` : pane.diffSummary || "—";
  return msg`変更を表示 ${{ target }} ${{ stat }}`;
}

/* 行 identity を組める行を、diff ビュアーへの直リンクにする。 */
function DiffCell({
  parent,
  pane,
  onOpenDiff,
}: {
  parent: string;
  pane: PaneView;
  onOpenDiff: (parent: string, pane: PaneView) => void;
}) {
  const { i18n, t } = useLingui();
  const d = parseDiff(pane.diffSummary ?? "");
  /* 解析できない summary(gitstat の一時失敗で "-" や自由文になる)でも、行
   * identity があれば diff は取れる。要約はそのままテキストで見せる。 */
  const stat = d ? (
    <>
      <span className="add">+{d.add}</span>/<span className="del">-{d.del}</span>
    </>
  ) : (
    <span className="muted">{pane.diffSummary || "—"}</span>
  );
  /* 行数を「差分あり」の判定には使わない。diffSummary と /api/diff は同じ
   * 収集を共有するので行数は一致するが、+0/-0 は「レビュー対象なし」を
   * 意味しない — binary だけ / mode だけ / pure rename の変更は両方で +0/-0
   * になり、commit 済みなら clean にもなる一方、/api/diff はそれらを全部
   * レビュー対象として返す。開けないほうが害が大きいので、identity を
   * 組める行は常にリンクにする(詳細ドロワーの「変更を表示」も同じ条件)。 */
  if (!diffQuery(parent, pane)) return stat;
  return (
    <button
      type="button"
      className="diff-link"
      title={t`変更を表示`}
      /* 名前に対象を入れる — 同じ統計の行が複数あると「変更を表示 +N/-M」が
         並んで、支援技術からどの行の diff か区別できない。
         issue / task だけでは足りない: 同じ parent の下で別の worktree が同じ
         task を持つと paneLabel も表示名も一致しうる(attached-agent が同じ
         source を指す兄弟でも同じ)。行を一意にしているのは worktree なので、
         それを名指す branch まで含める。名前が重なるのは同じ worktree を開く
         ボタン同士、つまり中身が同じときだけになる。 */
      aria-label={i18n._(diffLinkLabel(pane, d))}
      // 行クリック(Drawer を開く)には伝播させない — セルは diff への直行導線
      onClick={(e) => {
        e.stopPropagation();
        onOpenDiff(parent, pane);
      }}
    >
      {stat}
    </button>
  );
}

function PaneRow({
  parent,
  pane,
  repo,
  selected,
  onSelect,
  onOpenDiff,
  registerRow,
}: {
  parent: string;
  pane: PaneView;
  repo: string;
  selected: boolean;
  onSelect: () => void;
  onOpenDiff: (parent: string, pane: PaneView) => void;
  registerRow: (el: HTMLTableRowElement | null) => void;
}) {
  const ci = paneCI(pane);
  const wave = fmtWave(pane);
  const runtimeState = paneRuntimeState(pane);
  const runtimeTitle = paneRuntimeTitle(pane);
  const runtimeBackend = paneBackend(pane);
  const onClick = (e: MouseEvent) => {
    // GitHub リンク(issue / PR)は行選択にしない
    if ((e.target as Element).closest("a")) return;
    onSelect();
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    if (e.target !== e.currentTarget) return;
    e.preventDefault();
    onSelect();
  };
  // 未開始(synthetic)行は .ghost で一段引いた表示。rowKey は同じなので
  // pane が起動した snapshot からはそのまま実 row として描画される。
  const cls = `row${pane.notStarted ? " ghost" : ""}${selected ? " selected" : ""}`;
  return (
    <tr className={cls} tabIndex={0} ref={registerRow} onClick={onClick} onKeyDown={onKeyDown}>
      <td className="c-issue">
        <GhLink url={paneIssueURL(repo, pane)}>{paneLabel(pane)}</GhLink>
      </td>
      <td className="c-name" title={pane.slug}>
        {paneName(pane) || "—"}
      </td>
      <td>{pane.agent || "—"}</td>
      <td>{wave || <span className="muted">—</span>}</td>
      <td title={fmtBlockers(pane)}>
        <BlockersCell pane={pane} />
      </td>
      <td className="c-branch" title={pane.branchName}>
        {pane.branchName || "—"}
      </td>
      <td className={pane.worktreeErr ? "c-diff fault" : "c-diff"} title={pane.worktreeErr ?? ""}>
        <DiffCell parent={parent} pane={pane} onOpenDiff={onOpenDiff} />
      </td>
      <td>
        <DirtyTag state={pane.dirtyState} />
      </td>
      <td>
        <CiCell ci={ci} />
      </td>
      <td
        className="c-runtime"
        title={[runtimeBackend, pane.paneId, runtimeTitle].filter(Boolean).join(" · ")}
      >
        <span className="runtime-ref">
          <span className="runtime-backend">{runtimeBackend || "—"}</span>
          <span className="runtime-pane">{pane.paneId || "—"}</span>
        </span>
        <span className="runtime-status">
          <span className={pane.alive ? "dot on" : "dot off"} aria-hidden="true"></span>
          {pane.alive ? "live" : runtimeState || "stale"}
          {isKnownAgentState(pane.agentState) && (
            <>
              {" "}
              <AgentStateTag state={pane.agentState} />
            </>
          )}
        </span>
      </td>
      <td>
        <IssueStateTag state={pane.issueState} />
      </td>
      <td className="c-pr">
        <PrCell repo={repo} pr={prPrimary(pane.prs)} />
      </td>
    </tr>
  );
}

export interface SessionItem {
  parent: string;
  panes: PaneView[]; // filter + sort 済み
  rollup: Rollup;
  rise: boolean;
}

export function SessionSection({
  item,
  repo,
  sortKey,
  sortDir,
  selected,
  onSort,
  onSelect,
  onOpenDiff,
  registerRow,
}: {
  item: SessionItem;
  repo: string;
  sortKey: string;
  sortDir: SortDir;
  selected: string | null;
  onSort: (key: string) => void;
  onSelect: (key: string) => void;
  onOpenDiff: (parent: string, pane: PaneView) => void;
  registerRow: (key: string, el: HTMLTableRowElement | null) => void;
}) {
  const sr = item.rollup;
  const pct = sr.total ? Math.round((sr.merged / sr.total) * 100) : 0;
  return (
    <section className={item.rise ? "session rise" : "session"} data-parent={item.parent}>
      <header className="session-head">
        <h2>
          <span className="s-parent">
            <GhLink url={parentUrl(repo, item.parent)}>{parentLabel(item.parent)}</GhLink>
          </span>
        </h2>
        <div className="s-progress">
          <span>
            {sr.merged}/{sr.total} merged
          </span>
          <div className="bar">
            <i style={{ width: `${pct}%` }}></i>
          </div>
        </div>
      </header>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              {COLS.map(([key, label]) => (
                <th
                  key={key}
                  data-sort={key}
                  aria-sort={
                    sortKey === key ? (sortDir === 1 ? "ascending" : "descending") : "none"
                  }
                  onClick={() => onSort(key)}
                >
                  {label}
                  {sortKey === key && <span className="dir"> {sortDir === 1 ? "▴" : "▾"}</span>}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {item.panes.map((p) => {
              const key = rowKey(item.parent, p);
              return (
                <PaneRow
                  key={key}
                  parent={item.parent}
                  pane={p}
                  repo={repo}
                  selected={selected === key}
                  onSelect={() => onSelect(key)}
                  onOpenDiff={onOpenDiff}
                  registerRow={(el) => registerRow(key, el)}
                />
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
