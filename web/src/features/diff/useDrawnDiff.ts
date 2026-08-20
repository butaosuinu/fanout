import { useEffect } from "react";
import type { DiffState } from "../../transport/useDiff";
import type { DiffResponse } from "../../transport/types";

/* 描いている diff を確定し、そのまま親へ返す。
 *
 * 親が持つのは、行のマージボタンを塞ぐため — 表示中の patch がどの commit の
 * ものか、未コミットの変更を含むか、base は remote にあるか。読み込み中と失敗は
 * null になるので、対象を切り替えた瞬間に前の行の patch が残ることはない。 */
export function useDrawnDiff(
  state: DiffState,
  report: (diff: DiffResponse | null) => void,
): DiffResponse | null {
  const diff = state.phase === "ready" ? state.diff : null;
  useEffect(() => {
    report(diff);
  }, [report, diff]);
  return diff;
}
