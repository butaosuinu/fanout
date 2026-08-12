import { useCallback, useState } from "react";
import { useStableCallback } from "../../shared/useStableCallback";
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
  /* collapsed が null なら上書きを取り消して初期方針へ戻す。確認済みを外したとき
   * に `false` を書いてしまうと、1,000 行以上で既定折りたたみだった file が
   * 「開いたことすらない状態」から全開になり、読んでいた位置が飛ぶ。 */
  const setCollapsed = useCallback(
    (i: number, collapsed: boolean | null) => {
      setOv((prev) => {
        const map = new Map(prev.patch === patch ? prev.map : EMPTY_OVERRIDES);
        if (collapsed === null) map.delete(i);
        else map.set(i, collapsed);
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

  /* 「畳まれているか」の唯一の判定。描画側にも同じ式を書くと、上書き・確認済み・
   * 初期方針の優先順位が 2 箇所に増え、片方だけ変えたときにボタンと本文が食い違う。
   * identity は overrides / plan / viewed が変わったときだけ変わるので、描画側の
   * memo 境界もこれを渡して壊れない。 */
  const isCollapsed = useCallback(
    (i: number) => collapsedAt(plan[i], overrides.get(i), viewed.has(i)),
    [overrides, plan, viewed],
  );

  /* 行に降りるハンドラは identity を固定する。ここが毎 render 変わると
   * DiffFileRow の memo が全件で外れ、1 回のクリックが全 file の再描画になる。 */
  const onToggle = useStableCallback((i: number) => setCollapsed(i, !isCollapsed(i)));
  /* 折りたたまれていれば開く。すでに開いていれば何もしない — 無条件に書くと
   * 同じ状態のまま Map を差し替えて再レンダーを起こす。 */
  const expand = useCallback(
    (i: number) => {
      if (isCollapsed(i)) setCollapsed(i, false);
    },
    [isCollapsed, setCollapsed],
  );
  const onExpandAll = useCallback(() => setAll(false), [setAll]);
  const onCollapseAll = useCallback(() => setAll(true), [setAll]);

  return { isCollapsed, onToggle, expand, onExpandAll, onCollapseAll, setCollapsed };
}
