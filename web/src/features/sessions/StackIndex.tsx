import { createContext, use, useCallback, useMemo, type ReactNode } from "react";
import type { PaneView, Snapshot } from "../../transport/types";
import { rowKeyOf } from "./pane";
import { buildStackIndex, EMPTY_STACK_INDEX, type StackIndex } from "./stack";

const Ctx = createContext<StackIndex>(EMPTY_STACK_INDEX);
const SelectCtx = createContext<(pane: PaneView) => void>(() => {});

/* snapshot 全体の stack 索引を、テーブルの pr 列とドロワーへ配る。どちらも
 * Dashboard から 3 段下にあり、経由するだけの prop を足すより MergeSlot と同じく
 * context で届ける。snapshot は約 2 秒ごとに差し替わるので、組み直しもそれに
 * 合わせる。
 *
 * onSelect は行の選択。stack map の他の行の名前から、その行のドロワーへ切り替える
 * のに使う(索引の層は snapshot の pane そのものを持つ)。 */
export function StackIndexProvider({
  snap,
  onSelect,
  children,
}: {
  snap: Snapshot | null;
  onSelect: (key: string) => void;
  children: ReactNode;
}) {
  const index = useMemo(() => buildStackIndex(snap), [snap]);
  const select = useCallback(
    (pane: PaneView) => {
      const key = rowKeyOf(snap, pane);
      if (!key) return;
      onSelect(key);
      /* ドロワーは行ごとに作り直されるので、押した名前のボタンごと消えてフォーカスが
       * body に落ちる。切り替えた先のドロワーへ戻す。 */
      requestAnimationFrame(() => document.getElementById("drawer-close")?.focus());
    },
    [snap, onSelect],
  );
  return (
    <Ctx value={index}>
      <SelectCtx value={select}>{children}</SelectCtx>
    </Ctx>
  );
}

export function useStackIndex(): StackIndex {
  return use(Ctx);
}

/* 層を持つ行のドロワーへ切り替える。provider の外では何もしない。 */
export function useSelectStackOwner(): (pane: PaneView) => void {
  return use(SelectCtx);
}
