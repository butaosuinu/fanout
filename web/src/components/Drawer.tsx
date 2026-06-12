import { Fragment, useEffect } from "react";
import { usePeek } from "../hooks/usePeek";
import { fmtCreated } from "../lib/format";
import { issueUrl } from "../lib/github";
import { blockerLabel, fmtWave } from "../lib/pane";
import type { PaneView } from "../lib/types";
import { AgentStateTag, DirtyTag, GhLink, IssueStateTag, PrPill, Tag } from "./ui";

function PeekPanel({ pane, token }: { pane: PaneView; token: string }) {
  const peek = usePeek({ paneId: pane.paneId, alive: pane.alive }, token);
  return (
    <section className="d-sec">
      <h4>peek — 直近の出力</h4>
      <div className="terminal">
        <div className="term-bar">
          <i></i>
          <i></i>
          <i></i>
          <span className="term-title" id="peek-title">
            {pane.paneId} — {pane.agent || "?"}
          </span>
        </div>
        {/* capture 出力は敵性入力 — テキストノード以外で描画しないこと */}
        <pre id="peek-pre">{peek.output}</pre>
      </div>
      <div className="term-meta" id="peek-meta">
        {peek.meta}
      </div>
    </section>
  );
}

export function Drawer({
  pane,
  repo,
  token,
  onClose,
}: {
  pane: PaneView;
  repo: string;
  token: string;
  onClose: () => void;
}) {
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  return (
    <aside id="drawer" aria-label="ペイン詳細">
      <header className="drawer-head">
        <h3>
          <span className="d-issue" id="d-issue">
            <GhLink url={issueUrl(repo, pane.issueNum)}>#{pane.issueNum}</GhLink>
          </span>
          <span id="d-name">{pane.displayName || pane.slug || "—"}</span>
        </h3>
        <button id="drawer-close" type="button" aria-label="詳細を閉じる" onClick={onClose}>
          ✕
        </button>
      </header>
      <div className="drawer-body">
        <section className="d-sec">
          <h4>pane</h4>
          <dl className="d-kv">
            <dt>agent</dt>
            <dd id="d-agent">{pane.agent || "—"}</dd>
            <dt>pane</dt>
            <dd id="d-pane">{pane.paneId || "—"}</dd>
            <dt>tmux</dt>
            <dd id="d-tmux">{pane.alive ? "live" : pane.tmuxState || "stale"}</dd>
            <dt>run</dt>
            <dd id="d-run">
              {pane.agentState === "running" || pane.agentState === "done" ? (
                <AgentStateTag state={pane.agentState} />
              ) : (
                <span className="muted">—</span>
              )}
            </dd>
            <dt>title</dt>
            <dd id="d-title">{pane.tmuxTitle || "—"}</dd>
            <dt>created</dt>
            <dd id="d-created">{fmtCreated(pane.createdAt)}</dd>
            <dt>issue</dt>
            <dd id="d-state">
              <IssueStateTag state={pane.issueState} unknownLabel="UNKNOWN" />
            </dd>
          </dl>
        </section>
        <section className="d-sec">
          <h4>wave / blockers</h4>
          <dl className="d-kv">
            <dt>wave</dt>
            <dd id="d-wave">{fmtWave(pane) || "—"}</dd>
            <dt>blockers</dt>
            <dd id="d-blockers">
              {pane.blockers && pane.blockers.length
                ? pane.blockers.map((b, i) => (
                    <Fragment key={b.num}>
                      {i > 0 && ", "}
                      {blockerLabel(b)} <GhLink url={issueUrl(repo, b.num)}>#{b.num}</GhLink>
                    </Fragment>
                  ))
                : "-"}
            </dd>
          </dl>
        </section>
        <section className="d-sec">
          <h4>worktree</h4>
          <dl className="d-kv">
            <dt>path</dt>
            <dd id="d-path">
              {(pane.worktreePath || "—") + (pane.worktreeErr ? ` (${pane.worktreeErr})` : "")}
            </dd>
            <dt>branch</dt>
            <dd id="d-branch">{pane.branchName || "—"}</dd>
            <dt>diff</dt>
            <dd id="d-diff">{pane.diffSummary || "—"}</dd>
            <dt>state</dt>
            <dd id="d-dirty">
              <DirtyTag state={pane.dirtyState} unknownLabel="unknown" />
            </dd>
          </dl>
        </section>
        <section className="d-sec">
          <h4>pull requests</h4>
          <ul className="d-prs" id="d-prs">
            {pane.prs && pane.prs.length ? (
              pane.prs.map((pr) => (
                <li key={pr.number}>
                  <PrPill repo={repo} pr={pr} />
                  {pr.ci === "pass" ? (
                    <>
                      {" "}
                      <Tag cls="t-ok">ci pass</Tag>
                    </>
                  ) : pr.ci === "fail" ? (
                    <>
                      {" "}
                      <Tag cls="t-err">ci fail</Tag>
                    </>
                  ) : pr.ci ? (
                    <>
                      {" "}
                      <Tag cls="t-warn">ci pending</Tag>
                    </>
                  ) : null}
                  {pr.reviewDecision && (
                    <>
                      {" "}
                      <Tag>{pr.reviewDecision.toLowerCase().replace(/_/g, " ")}</Tag>
                    </>
                  )}
                </li>
              ))
            ) : (
              <li className="muted">—</li>
            )}
          </ul>
        </section>
        <section className="d-sec">
          <h4>prompt</h4>
          <pre className="d-prompt" id="d-prompt">
            {pane.prompt || "—"}
          </pre>
        </section>
        <PeekPanel pane={pane} token={token} />
      </div>
    </aside>
  );
}
