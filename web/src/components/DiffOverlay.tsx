import { PatchDiff } from "@pierre/diffs/react";
import { useEffect, useMemo, useRef } from "react";
import { useDiff } from "../hooks/useDiff";
import { useTheme } from "../hooks/useTheme";
import {
  diffTotals,
  diffWarning,
  omittedFiles,
  OMITTED_REASON_LABELS,
  splitPatch,
} from "../lib/diff";
import { clock } from "../lib/format";
import type { DiffResponse } from "../lib/types";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

function DiffFiles({ diff, theme }: { diff: DiffResponse; theme: "light" | "dark" }) {
  const blocks = useMemo(() => splitPatch(diff.patch), [diff.patch]);
  /* themeType を渡すと library 側が light/dark の Shiki テーマを切り替える。
   * worker pool は使わない(Provider 非設置 = メインスレッド描画)。 */
  const options = useMemo(() => ({ themeType: theme }), [theme]);
  const omitted = omittedFiles(diff.files);
  return (
    <>
      {blocks.map((block, i) => (
        // block 順は path 順で response ごとに固定 — index key で安定
        <PatchDiff key={i} patch={block} options={options} className="diff-file" />
      ))}
      {omitted.length > 0 && (
        <section className="diff-omitted" aria-label="patch が省略されたファイル">
          <h4>patch が省略されたファイル</h4>
          <ul>
            {omitted.map((f) => (
              <li key={f.path}>
                <code>{f.path}</code>
                <span className="muted">
                  {" — "}
                  {f.omittedReason ? OMITTED_REASON_LABELS[f.omittedReason] : "省略"}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

export function DiffOverlay({
  title,
  query,
  token,
  onClose,
}: {
  title: string;
  query: Record<string, string>;
  token: string;
  onClose: () => void;
}) {
  const { theme } = useTheme();
  const { state, refetch } = useDiff(query, token);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    rootRef.current?.focus();
  }, []);

  /* capture 段で preventDefault を立て、Drawer の document(bubble)listener に
   * Escape を渡さない — オーバーレイだけを閉じ、下の Drawer は開いたまま残す。 */
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) {
        e.preventDefault();
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [onClose]);

  const diff = state.phase === "ready" ? state.diff : null;
  const warning = diff ? diffWarning(diff) : null;
  let meta = "";
  if (diff) {
    const totals = diffTotals(diff.files);
    meta =
      `merge-base ${diff.mergeBase.slice(0, 10)} · captured ${clock(diff.capturedAt)}` +
      ` · ${diff.files.length} files +${totals.additions}/-${totals.deletions}`;
  }

  return (
    <div
      className="diff-overlay"
      id="diff-overlay"
      role="dialog"
      aria-modal="true"
      aria-label="worktree diff"
      data-theme={theme}
      ref={rootRef}
      tabIndex={-1}
    >
      <header className="diff-head">
        <h3>
          <span className="diff-title">{title}</span>
          {diff && (
            <span className="diff-branches">
              <code>{diff.branchName}</code> → <code>{diff.baseBranch}</code>
            </span>
          )}
        </h3>
        <span className="diff-meta" id="diff-meta">
          {meta}
        </span>
        <span className="grow"></span>
        <button
          type="button"
          className="diff-reload"
          onClick={refetch}
          disabled={state.phase === "loading"}
        >
          再取得
        </button>
        <button type="button" id="diff-close" aria-label="diff を閉じる" onClick={onClose}>
          ✕
        </button>
      </header>
      {warning && (
        <div className="diff-banner" role="status">
          {warning}
        </div>
      )}
      <div className="diff-body">
        {state.phase === "loading" && <div className="diff-note">diff を取得中…(最大 10 秒)</div>}
        {state.phase === "error" && (
          <div className="diff-note diff-error" role="alert">
            {state.message}
          </div>
        )}
        {diff &&
          (diff.files.length === 0 ? (
            <div className="diff-note">merge-base からの変更はありません</div>
          ) : (
            <DiffFiles diff={diff} theme={theme} />
          ))}
      </div>
    </div>
  );
}
