import { useMemo } from "react";
import { indexDiffFilesByPath, indexDiffKindsByPath, parseDiffFiles, planDiffFiles } from "./diff";

/* patch 文字列から、描画に要る派生物をまとめて作る。
 *
 * 4 つとも「patch をパースした結果」の別の切り口で、依存も parsed 1 本に揃う。
 * 呼び出し側に useMemo を並べると、どれが patch に依存していてどれが派生の派生か
 * が読み取れなくなり、snapshot の tick ごとに全走査をやり直す事故も起きやすい。
 *
 * parsed 自体は返さない — 外へ出すと「もう 1 つ派生を足す」場所が 2 箇所になる。 */
export function useDiffPatch(patch: string) {
  const parsed = useMemo(() => parseDiffFiles(patch), [patch]);
  const plan = useMemo(() => planDiffFiles(parsed), [parsed]);
  const byPath = useMemo(() => indexDiffFilesByPath(parsed), [parsed]);
  /* 本文へ飛べる path。patch にブロックがある file だけが持つ */
  const selectable = useMemo(() => new Set(byPath.keys()), [byPath]);
  const kinds = useMemo(() => indexDiffKindsByPath(parsed), [parsed]);
  return { plan, byPath, selectable, kinds };
}
