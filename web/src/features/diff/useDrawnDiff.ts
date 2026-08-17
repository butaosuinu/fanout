import { useEffect } from "react";
import type { DiffState } from "../../transport/useDiff";
import type { DiffResponse } from "../../transport/types";

/* 描いている diff を確定し、それを取った commit を親へ返す。
 *
 * 親が持つのは、行のマージボタンを塞ぐため — 表示中の patch がどの commit の
 * ものかは、ここでしか分からない。読み込み中と失敗は "" になるので、対象を
 * 切り替えた瞬間に前の行の commit が残ることはない。 */
export function useDrawnDiff(
  state: DiffState,
  report: (commit: string) => void,
): DiffResponse | null {
  const diff = state.phase === "ready" ? state.diff : null;
  useEffect(() => {
    report(diff?.headCommit ?? "");
  }, [report, diff]);
  return diff;
}
