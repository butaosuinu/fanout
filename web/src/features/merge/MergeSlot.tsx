import { createContext, use, type ReactNode } from "react";
import type { MergeAffordance } from "./useMergeFlow";

const Ctx = createContext<MergeAffordance | null>(null);

/* 1 つの行のマージボタンの状態を、その行を描いている部分木へ配る。
 *
 * props で降ろさないのは、diff ツールバーが DiffOverlay の内側に居るから。
 * 経由するだけの prop を DiffOverlay に足すと、あの 150 行の関数がさらに伸びる。
 * 配る値は行ごとに 1 つで、provider は行を描く境界(ドロワー / diff オーバーレイ)
 * にだけ立つ。 */
export function MergeSlot({
  value,
  children,
}: {
  value: MergeAffordance | null;
  children: ReactNode;
}) {
  return <Ctx value={value}>{children}</Ctx>;
}

/* 現在の行のマージボタン。PR が無い行と、provider の外では null。 */
export function useMergeSlot(): MergeAffordance | null {
  return use(Ctx);
}
