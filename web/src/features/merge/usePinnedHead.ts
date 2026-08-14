import { useCallback, useRef } from "react";
import { MERGE_STALE_DIFF } from "./merge";
import type { MergeAffordance } from "./useMergeFlow";

/* diff ビュアーが開いた時点の PR head を固定し、ズレている間はその行のマージを
 * 塞ぐ。
 *
 * 3 段照合(クライアントの番号+SHA / snapshot 行 / --match-head-commit)は、
 * 「クライアントが今見ている PR」を基準にしている。ところが diff は明示的な
 * 再取得まで最初の patch を保持するので、開いたまま agent が push すると 3 つとも
 * 新しい head で一致してしまい、ユーザーが見ていない変更がそのまま通る。
 *
 * 塞ぐのは行単位で、diff ツールバーと Drawer の両方。compact 表示では 2 つが
 * 並ぶので、片方だけ塞いでも横のボタンから同じマージが撃てる。
 *
 * pin は「今開いている diff」の寿命に紐づく。解除は再取得ではなく開き直し —
 * 再取得の完了は DiffOverlay の内側にしか無く、それをここまで引き回すと経由する
 * だけの prop が DiffOverlay に増える。
 *
 * 束縛できるのは「この PR の head が開いた時点から動いたか」までで、「画面の
 * patch がその head の中身か」ではない。/api/diff は worktree の差分を返すので、
 * PR head とは別の対象である(fork PR の行では特にそう)。読んでいる最中の push を
 * 捕まえるのが目的で、それ以上を主張しない。 */
export function usePinnedHead(
  diffKey: string | null,
): (rowKey: string, merge: MergeAffordance | null) => MergeAffordance | null {
  const pinned = useRef<{ key: string; headSha: string } | null>(null);

  return useCallback(
    (rowKey, merge) => {
      /* 閉じたら捨てる。残すと、同じ行を新しい diff で開き直しても key が同じ
       * ままなので pin が更新されず、間に head が進んでいた行が永久に stale
       * 扱いになる(開き直せば実行できる、という契約に反する)。 */
      if (diffKey === null) pinned.current = null;
      if (!merge || diffKey === null || rowKey !== diffKey) return merge;
      if (pinned.current?.key !== diffKey) {
        pinned.current = { key: diffKey, headSha: merge.headSha };
      }
      if (pinned.current.headSha === merge.headSha) return merge;
      return { ...merge, blocked: MERGE_STALE_DIFF, warnings: [] };
    },
    [diffKey],
  );
}
