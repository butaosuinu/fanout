import { createContext, use, useMemo, type ReactNode } from "react";
import type { Snapshot } from "../../transport/types";
import { buildStackIndex, EMPTY_STACK_INDEX, type StackIndex } from "./stack";

const Ctx = createContext<StackIndex>(EMPTY_STACK_INDEX);

/* snapshot 全体の stack 索引を、テーブルの pr 列とドロワーへ配る。どちらも
 * Dashboard から 3 段下にあり、経由するだけの prop を足すより MergeSlot と同じく
 * context で届ける。snapshot は約 2 秒ごとに差し替わるので、組み直しもそれに
 * 合わせる。 */
export function StackIndexProvider({
  snap,
  children,
}: {
  snap: Snapshot | null;
  children: ReactNode;
}) {
  const index = useMemo(() => buildStackIndex(snap), [snap]);
  return <Ctx value={index}>{children}</Ctx>;
}

export function useStackIndex(): StackIndex {
  return use(Ctx);
}
