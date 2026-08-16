import type { MessageDescriptor } from "@lingui/core";
import { useCallback, useRef } from "react";
import { MERGE_DIFF_MISMATCH, MERGE_STALE_DIFF } from "./merge";
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
): (rowKey: string, merge: MergeAffordance | null, shows: DiffSource) => MergeAffordance | null {
  const pinned = useRef<Pinned | null>(null);

  return useCallback(
    (rowKey, merge, shows) => {
      /* 閉じたら捨てる。残すと、同じ行を新しい diff で開き直しても key が同じ
       * ままなので pin が更新されず、間に head が進んでいた行が永久に stale
       * 扱いになる(開き直せば実行できる、という契約に反する)。 */
      if (diffKey === null) pinned.current = null;
      if (!merge || diffKey === null || rowKey !== diffKey) return merge;
      if (pinned.current?.key !== diffKey) {
        pinned.current = { key: diffKey, prNumber: merge.prNumber, headSha: merge.headSha };
      }
      const reason = mismatch(merge, shows, pinned.current);
      return reason ? { ...merge, blocked: reason, warnings: [] } : merge;
    },
    [diffKey],
  );
}

/* diff ビュアーが読んでいる worktree の repository / branch と、patch を計算した
 * base branch。 */
export type DiffSource = { repo: string; branch: string; base: string };

/* diff を開いた時点の対象 PR。 */
type Pinned = { key: string; prNumber: number; headSha: string };

/* 画面の patch と、これからマージされるものがズレる 2 通り。塞がないなら null。 */
function mismatch(
  merge: MergeAffordance,
  shows: DiffSource,
  pin: Pinned,
): MessageDescriptor | null {
  /* 別物: 表示している patch は pane の worktree のもの。対象 PR の head がその
   * branch でないなら、画面の内容と別物をマージすることになる(issue 行に fork の
   * closing PR が載っている場合など)。head が動いたかを見る pin では捕まらない。
   *
   * base も同じ理由で見る。patch は worktree の base branch との差分なので、PR が
   * 別の base へ retarget されると、head が 1 commit も動かないまま「画面に出て
   * いない差分」がマージ対象になる。head だけの pin はこれを通してしまう。 */
  if (!sameSource(merge, shows)) return MERGE_DIFF_MISMATCH;
  /* すり替え: 行の対象 PR そのものが入れ替わった(新しい PR が open になった等)。 */
  if (pin.prNumber !== merge.prNumber) return MERGE_DIFF_MISMATCH;
  /* 追い越し: 読んでいる間に push された。 */
  if (pin.headSha !== merge.headSha) return MERGE_STALE_DIFF;
  return null;
}

/* 空の記録は判定材料が無いという意味で、一致とみなす(塞ぐ根拠にしない)。 */
function sameSource(merge: MergeAffordance, shows: DiffSource): boolean {
  if (shows.base && merge.baseRef !== shows.base) return false;
  if (!shows.branch) return true;
  if (merge.headRef !== shows.branch) return false;
  return !!merge.headRepo && merge.headRepo.toLowerCase() === shows.repo.toLowerCase();
}
