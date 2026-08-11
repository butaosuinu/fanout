import { useCallback, useState } from "react";
import type { DiffFilePlan } from "./diff";

const EMPTY_OVERRIDES: ReadonlyMap<number, boolean> = new Map();

/* patch ごとの上書き。patch が変われば index の意味も変わるので、両者を組で持つ。 */
type Overrides = { patch: string; map: ReadonlyMap<number, boolean> };

/* いま折りたたまれているか。上書きが無ければ「確認済みなら畳む」、それも無ければ
 * 初期方針(planDiffFiles)に従う。確認済みをここに通さないと、保存値から復元した
 * file の展開ボタンが no-op になる — override 不在で initiallyCollapsed=false を
 * 見て「いまは展開中」と判断し、押すと true を書いて畳んだままになる。 */
function collapsedAt(
  entry: DiffFilePlan | undefined,
  override: boolean | undefined,
  viewed: boolean,
): boolean {
  return override ?? (viewed || (entry?.initiallyCollapsed ?? false));
}

/* 初期方針(planDiffFiles)に対するユーザーの上書き。複数 file を同時に展開した
 * ままにできる。patch 自体を key に持つ — 再取得で patch が変わると index の
 * 意味も変わるが、passive effect でのリセットでは間に合わない(@pierre/diffs は
 * ref/layout 経路で同期 mount するので、リセット前の 1 回で古い index の
 * 折りたたみ状態を新しい file に適用してしまう)。 */
export function useDiffCollapse(patch: string, plan: DiffFilePlan[], viewed: ReadonlySet<number>) {
  const [ov, setOv] = useState<Overrides>({ patch, map: EMPTY_OVERRIDES });
  const overrides = ov.patch === patch ? ov.map : EMPTY_OVERRIDES;
  const setCollapsed = useCallback(
    (i: number, collapsed: boolean) => {
      setOv((prev) => {
        const map = new Map(prev.patch === patch ? prev.map : EMPTY_OVERRIDES);
        map.set(i, collapsed);
        return { patch, map };
      });
    },
    [patch],
  );
  const setAll = useCallback(
    (collapsed: boolean) => {
      const map = new Map<number, boolean>();
      plan.forEach((_, i) => map.set(i, collapsed));
      setOv({ patch, map });
    },
    [plan, patch],
  );

  const onToggle = useCallback(
    (i: number) => setCollapsed(i, !collapsedAt(plan[i], overrides.get(i), viewed.has(i))),
    [overrides, plan, setCollapsed, viewed],
  );
  /* 折りたたまれていれば開く。すでに開いていれば何もしない — 無条件に書くと
   * 同じ状態のまま Map を差し替えて再レンダーを起こす。 */
  const expand = useCallback(
    (i: number) => {
      if (collapsedAt(plan[i], overrides.get(i), viewed.has(i))) setCollapsed(i, false);
    },
    [overrides, plan, setCollapsed, viewed],
  );
  const onExpandAll = useCallback(() => setAll(false), [setAll]);
  const onCollapseAll = useCallback(() => setAll(true), [setAll]);

  return { overrides, onToggle, expand, onExpandAll, onCollapseAll, setCollapsed };
}
