import type { KeyboardEvent, MouseEvent } from "react";
import { parentLabel, parentUrl } from "../lib/github";
import { parseDiff } from "../lib/format";
import {
  blockersAllClosed,
  fmtBlockers,
  fmtWave,
  openBlockerCount,
  paneCI,
  paneIssueURL,
  paneLabel,
  prPrimary,
  rowKey,
} from "../lib/pane";
import { COLS, type SortDir } from "../lib/sort";
import type { PaneView, Rollup } from "../lib/types";
import { AgentStateTag, DirtyTag, GhLink, IssueStateTag, PrPill, Tag } from "./ui";

function BlockersCell({ pane }: { pane: PaneView }) {
  if (pane.blocked) return <Tag cls="t-warn">{`${openBlockerCount(pane)} open`}</Tag>;
  if (!pane.blockers || !pane.blockers.length) return <span className="muted">—</span>;
  return <span className="muted">{blockersAllClosed(pane) ? "resolved" : "unknown"}</span>;
}

function DiffCell({ pane }: { pane: PaneView }) {
  const d = parseDiff(pane.diffSummary ?? "");
  if (!d) return <span className="muted">{pane.diffSummary || "—"}</span>;
  return (
    <>
      <span className="add">+{d.add}</span>/<span className="del">-{d.del}</span>
    </>
  );
}

function PaneRow({
  pane,
  repo,
  selected,
  onSelect,
  registerRow,
}: {
  pane: PaneView;
  repo: string;
  selected: boolean;
  onSelect: () => void;
  registerRow: (el: HTMLTableRowElement | null) => void;
}) {
  const ci = paneCI(pane);
  const wave = fmtWave(pane);
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
        {pane.derived?.name || pane.displayName || pane.slug || "—"}
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
        <DiffCell pane={pane} />
      </td>
      <td>
        <DirtyTag state={pane.dirtyState} />
      </td>
      <td>
        {ci === "pass" ? (
          <Tag cls="t-ok">pass</Tag>
        ) : ci === "fail" ? (
          <Tag cls="t-err">fail</Tag>
        ) : ci === "pending" ? (
          <Tag cls="t-warn">pending</Tag>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
      <td title={pane.tmuxTitle ?? ""}>
        <span className={pane.alive ? "dot on" : "dot off"} aria-hidden="true"></span>
        {pane.alive ? "live" : pane.tmuxState || "stale"}
        {(pane.agentState === "running" || pane.agentState === "done") && (
          <>
            {" "}
            <AgentStateTag state={pane.agentState} />
          </>
        )}
      </td>
      <td>
        <IssueStateTag state={pane.issueState} />
      </td>
      <td>
        <PrPill repo={repo} pr={prPrimary(pane.prs)} />
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
  registerRow,
}: {
  item: SessionItem;
  repo: string;
  sortKey: string;
  sortDir: SortDir;
  selected: string | null;
  onSort: (key: string) => void;
  onSelect: (key: string) => void;
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
                  pane={p}
                  repo={repo}
                  selected={selected === key}
                  onSelect={() => onSelect(key)}
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
