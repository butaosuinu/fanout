import { Fragment, useEffect, type CSSProperties } from "react";
import { useDrawerWidth } from "../hooks/useDrawerWidth";
import { usePeek } from "../hooks/usePeek";
import { usePlan } from "../hooks/usePlan";
import { fmtCreated } from "../lib/format";
import { issueUrl } from "../lib/github";
import {
  blockerLabel,
  fmtWave,
  notStartedNote,
  paneBackend,
  paneIssueURL,
  paneLabel,
  paneRuntimeState,
  paneRuntimeTitle,
} from "../lib/pane";
import type { PaneView } from "../lib/types";
import {
  AgentStateTag,
  DirtyTag,
  GhLink,
  isKnownAgentState,
  IssueStateTag,
  PrPill,
  Tag,
} from "./ui";

function PlanPanel({ pane, token }: { pane: PaneView; token: string }) {
  const plan = usePlan({ paneId: pane.paneId, alive: pane.alive }, token);
  return (
    <section className="d-sec">
      <h4>plan — 提案中のプラン</h4>
      {/* capture 出力は敵性入力 — markdown レンダせずテキストノードのみで描画。
          tabIndex: 長い plan のスクロールをキーボードで届くように */}
      <pre
        className="plan-card"
        id="plan-pre"
        tabIndex={0}
        role="region"
        aria-label="提案中のプラン"
      >
        {plan.text}
      </pre>
      <div className="plan-meta" id="plan-meta" aria-live="polite">
        <span>{plan.loading ? "取得中…" : plan.meta}</span>
        <button
          type="button"
          className="plan-reload"
          onClick={plan.refetch}
          disabled={plan.loading}
        >
          再取得
        </button>
      </div>
    </section>
  );
}

/* 未開始(synthetic)行の状態説明。tmuxState は Go 側 syntheticTmuxState の
 * closed / deferred / queued / unknown と 1:1。 */
function WaveSection({ pane, repo }: { pane: PaneView; repo: string }) {
  return (
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
  );
}

function PrsSection({ pane, repo }: { pane: PaneView; repo: string }) {
  return (
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
  );
}

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

type CaptureKind = "peek" | "plan";

function captureDisabledReason(pane: PaneView): string | null {
  const runtimeBackend = paneBackend(pane);
  if (runtimeBackend === "herdr") {
    return "herdr backend v1 はペイン内容を読み取らないため利用できません。";
  }
  if (runtimeBackend !== "tmux") {
    return `${runtimeBackend} backend はペイン内容の読み取りに対応していません。`;
  }
  if (pane.derived?.canPeek === false) {
    return "この runtime pane は現在読み取り対象にできません。";
  }
  return null;
}

function CaptureDisabled({ kind, reason }: { kind: CaptureKind; reason: string }) {
  return (
    <section className="d-sec capture-disabled" aria-label={`${kind} disabled`} aria-disabled>
      <h4>{kind === "peek" ? "peek — 直近の出力" : "plan — 提案中のプラン"}</h4>
      <div className="capture-disabled-card">
        <span className="capture-disabled-mark">disabled</span>
        <span>{reason}</span>
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
  const { width, gripProps } = useDrawerWidth();
  const runtimeBackend = paneBackend(pane);
  const captureReason = captureDisabledReason(pane);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      // defaultPrevented = 手前のオーバーレイ(フィルタ popover 等)が消費済み
      if (e.key === "Escape" && !e.defaultPrevented) onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  /* 幅は CSS 変数 --drawer-w 経由でのみ反映する(inline width にすると
   * ≤820px の bottom sheet など media query が cascade で勝てなくなる)。 */
  return (
    <aside
      id="drawer"
      aria-label="ペイン詳細"
      style={{ "--drawer-w": `${width}px` } as CSSProperties}
    >
      {/* role / aria / tabIndex は hook が幅と一体で提供する(スプレッド漏れで
          セパレータ意味論だけ落ちる事故を防ぐ) */}
      <div className="drawer-grip" {...gripProps} />
      <header className="drawer-head">
        <h3>
          <span className="d-issue" id="d-issue">
            <GhLink url={paneIssueURL(repo, pane)}>{paneLabel(pane)}</GhLink>
          </span>
          <span id="d-name">{pane.derived?.name || pane.displayName || pane.slug || "—"}</span>
        </h3>
        <button id="drawer-close" type="button" aria-label="詳細を閉じる" onClick={onClose}>
          ✕
        </button>
      </header>
      {pane.notStarted ? (
        /* 未開始(synthetic)行の縮約表示: pane / worktree / prompt / peek は
         * 実体が無いので出さない(PeekPanel を mount しない = /api/peek 不発)。
         * pane が起動すると同じ rowKey のままこの分岐が実 row 表示に切り替わる。 */
        <div className="drawer-body">
          <section className="d-sec">
            <h4>issue</h4>
            <dl className="d-kv">
              <dt>issue</dt>
              <dd id="d-state">
                <IssueStateTag state={pane.issueState} unknownLabel="UNKNOWN" />
              </dd>
              <dt>状態</dt>
              <dd id="d-not-started">{notStartedNote(pane.tmuxState)}</dd>
            </dl>
          </section>
          <WaveSection pane={pane} repo={repo} />
          <PrsSection pane={pane} repo={repo} />
        </div>
      ) : (
        <div className="drawer-body">
          <section className="d-sec">
            <h4>pane</h4>
            <dl className="d-kv">
              <dt>agent</dt>
              <dd id="d-agent">{pane.agent || "—"}</dd>
              <dt>backend</dt>
              <dd id="d-backend">{runtimeBackend}</dd>
              <dt>pane</dt>
              <dd id="d-pane">{pane.paneId || "—"}</dd>
              <dt>runtime</dt>
              <dd id="d-tmux">{pane.alive ? "live" : paneRuntimeState(pane) || "stale"}</dd>
              <dt>run</dt>
              <dd id="d-run">
                {isKnownAgentState(pane.agentState) ? (
                  <AgentStateTag state={pane.agentState} />
                ) : (
                  <span className="muted">—</span>
                )}
              </dd>
              <dt>title</dt>
              <dd id="d-title">{paneRuntimeTitle(pane) || "—"}</dd>
              <dt>created</dt>
              <dd id="d-created">{fmtCreated(pane.createdAt)}</dd>
              <dt>issue</dt>
              <dd id="d-state">
                <IssueStateTag state={pane.issueState} unknownLabel="UNKNOWN" />
              </dd>
            </dl>
          </section>
          <WaveSection pane={pane} repo={repo} />
          <section className="d-sec">
            <h4>worktree</h4>
            <dl className="d-kv">
              <dt>path</dt>
              <dd id="d-path">
                {(pane.derived?.worktreeRelative || pane.worktreePath || "—") +
                  (pane.worktreeErr ? ` (${pane.worktreeErr})` : "")}
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
          <PrsSection pane={pane} repo={repo} />
          <section className="d-sec">
            <h4>prompt</h4>
            <pre className="d-prompt" id="d-prompt">
              {pane.prompt || "—"}
            </pre>
          </section>
          {pane.planMode &&
            (captureReason ? (
              <CaptureDisabled kind="plan" reason={captureReason} />
            ) : (
              <PlanPanel pane={pane} token={token} />
            ))}
          {captureReason ? (
            <CaptureDisabled kind="peek" reason={captureReason} />
          ) : (
            <PeekPanel pane={pane} token={token} />
          )}
        </div>
      )}
    </aside>
  );
}
