import { FileDiff } from "@pierre/diffs/react";
import { memo, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useDiff } from "../hooks/useDiff";
import { useTheme, type Theme } from "../hooks/useTheme";
import { apiUrl } from "../lib/api";
import {
  diffMeta,
  diffWarning,
  omittedFiles,
  OMITTED_REASON_LABELS,
  parseDiffFiles,
  planFileRendering,
} from "../lib/diff";
import type { DiffFileEntry, DiffResponse } from "../lib/types";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

/* memo: Drawer は SSE snapshot tick(約 2s)ごとに再レンダーされるが、diff と
 * theme は変わらないので file 列全体をスキップさせる(library の FileDiff は
 * 非 memo で、素通しすると tick ごとに全 file の setOptions/render が走る)。 */
const DiffFiles = memo(function DiffFiles({
  diff,
  omitted,
  theme,
}: {
  diff: DiffResponse;
  omitted: DiffFileEntry[];
  theme: Theme;
}) {
  const plan = useMemo(() => planFileRendering(parseDiffFiles(diff.patch)), [diff.patch]);
  /* 予算超過 file のうちユーザーが明示的に展開したもの(plan の index)。
   * 再取得で patch が変われば index の意味が変わるのでリセットする。 */
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  useEffect(() => setExpanded(new Set()), [diff.patch]);
  return (
    <>
      {plan.map(({ file, lines, overBudget }, i) => {
        const collapsed = overBudget && !expanded.has(i);
        return (
          // file type change は同 path が 2 entry になるため path 単独を key にしない
          <FileDiff
            key={`${i}:${file.name}`}
            fileDiff={file}
            options={{ themeType: theme, collapsed }}
            className="diff-file"
            renderHeaderMetadata={
              collapsed
                ? () => (
                    <button
                      type="button"
                      className="diff-expand"
                      onClick={() => setExpanded((prev) => new Set(prev).add(i))}
                    >
                      {lines.toLocaleString()} 行 — 展開
                    </button>
                  )
                : undefined
            }
          />
        );
      })}
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
});

export function DiffOverlay({
  title,
  query,
  token,
  onClose,
  onClosed,
}: {
  title: string;
  query: Record<string, string>;
  token: string;
  onClose: () => void;
  /* unmount 時、背面の inert 解除「後」に呼ぶ(起点ボタンへのフォーカス復帰用。
   * inert な subtree への focus は実ブラウザで拒否されるため順序が本質)。 */
  onClosed?: () => void;
}) {
  const { theme } = useTheme();
  const { state, refetch } = useDiff(apiUrl("/api/diff", token, query));
  const rootRef = useRef<HTMLDivElement>(null);
  /* ref 経由で最新を呼ぶ — effect を [] のまま保ち、再実行による inert の
   * 瞬断を避ける */
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;

  /* モーダル化: 初期フォーカスを移し、背面(#root 配下の Nav / テーブル /
   * Drawer)を inert にしてフォーカスと操作を遮る。閉じたら解除。 */
  useEffect(() => {
    rootRef.current?.focus();
    const root = document.getElementById("root");
    root?.setAttribute("inert", "");
    return () => {
      root?.removeAttribute("inert");
      onClosedRef.current?.();
    };
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
  /* snapshot tick ごとの再レンダーで files 全走査をやり直さない */
  const view = useMemo(
    () =>
      diff
        ? { warning: diffWarning(diff), meta: diffMeta(diff), omitted: omittedFiles(diff.files) }
        : null,
    [diff],
  );

  /* #drawer(sticky/fixed)はスタッキングコンテキストを作るため、その子として
   * 描くと z-index が閉じ込められ nav の下に潜る。portal で body 直下に出す。 */
  return createPortal(
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
          {view?.meta ?? ""}
        </span>
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
      {view?.warning && (
        <div className="diff-banner" role="status">
          {view.warning}
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
          view &&
          (diff.files.length === 0 ? (
            <div className="diff-note">merge-base からの変更はありません</div>
          ) : (
            <DiffFiles diff={diff} omitted={view.omitted} theme={theme} />
          ))}
      </div>
    </div>,
    document.body,
  );
}
