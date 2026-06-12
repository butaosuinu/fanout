import type { CSSProperties } from "react";
import type { Rollup } from "../lib/types";

const EMPTY_ROLLUP: Pick<
  Rollup,
  "total" | "merged" | "pending" | "live" | "running" | "blocked" | "notStarted"
> = {
  total: 0,
  merged: 0,
  pending: 0,
  live: 0,
  running: 0,
  blocked: 0,
  notStarted: 0,
};

export function Hud({ rollup }: { rollup: Rollup | null | undefined }) {
  const r = rollup ?? EMPTY_ROLLUP;
  const pct = r.total ? (r.merged / r.total) * 100 : 0;
  return (
    <>
      <section className="hud rise" style={{ "--d": ".05s" } as CSSProperties} aria-label="集計">
        <div className="stat">
          <label>panes</label>
          <b id="s-total">{r.total}</b>
        </div>
        <div className="stat">
          <label>live</label>
          <b id="s-live">{r.live}</b>
        </div>
        <div className="stat">
          <label>running</label>
          <b id="s-running">{r.running ?? 0}</b>
        </div>
        <div className="stat">
          <label>queued</label>
          {/* 未開始(synthetic)行数。旧 snapshot にはフィールドが無いので ?? 0 */}
          <b id="s-queued">{r.notStarted ?? 0}</b>
        </div>
        <div className="stat">
          <label>merged</label>
          <b id="s-merged">{r.merged}</b>
        </div>
        <div className="stat">
          <label>pending</label>
          <b id="s-pending">{r.pending}</b>
        </div>
        <div className="stat warned">
          <label>blocked</label>
          <b id="s-blocked">{r.blocked}</b>
        </div>
      </section>
      <div className="hud-bar rise" style={{ "--d": ".1s" } as CSSProperties} aria-hidden="true">
        <i id="hud-fill" style={{ width: `${pct}%` }}></i>
      </div>
    </>
  );
}
