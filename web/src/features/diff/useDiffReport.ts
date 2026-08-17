import { useState } from "react";

/* diff オーバーレイが親へ返すもの。
 *
 * covering は隠れた peek のポーリングを止めるため、headCommit は表示中の patch を
 * 取った commit を行のマージボタンに渡すため。どちらもオーバーレイの中でしか
 * 分からないので、state はここに置いて setter を降ろす。 */
export interface DiffReport {
  covering: boolean;
  /* useMergeFlow に渡す形。開いている行と、その patch を取った commit。 */
  shown: { key: string | null; head: string };
  setCovering: (covering: boolean) => void;
  setHeadCommit: (commit: string) => void;
}

export function useDiffReport(key: string | null): DiffReport {
  const [covering, setCovering] = useState(false);
  const [head, setHeadCommit] = useState("");
  return { covering, shown: { key, head }, setCovering, setHeadCommit };
}
