import type { MessageDescriptor } from "@lingui/core";
import { useCallback, useEffect, useRef } from "react";
import {
  MERGE_DIFF_LOCAL_BASE,
  MERGE_DIFF_MISMATCH,
  MERGE_DIFF_UNCOMMITTED,
  MERGE_STALE_DIFF,
} from "./merge";
import type { DiffFacts } from "../diff/useDiffReport";
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

  /* 閉じたら捨てる。残すと、同じ行を新しい diff で開き直しても key が同じままなので
   * pin が更新されず、間に head が進んでいた行が永久に stale 扱いになる(開き直せば
   * 実行できる、という契約に反する)。
   *
   * callback の中ではなく effect で捨てるのは、閉じた後にその行の callback が
   * 呼ばれる保証が無いから — 表の diff セルから直接開いた行にはドロワーが無く、
   * 閉じても誰もマージボタンを問い合わせない。 */
  useEffect(() => {
    if (diffKey === null) pinned.current = null;
  }, [diffKey]);

  return useCallback(
    (rowKey, merge, shows) => {
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
export type DiffSource = {
  repo: string;
  branch: string;
  base: string;
  /* diff 応答が言う、その patch の素性(commit / dirty / basePushed)。 */
  facts: DiffFacts;
};

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
  /* 過剰: /api/diff は staged / unstaged / untracked も描く。dirty な worktree では、
   * 画面で確認した修正のうち commit 済みの分だけがマージされる。 */
  if (shows.facts.dirty) return MERGE_DIFF_UNCOMMITTED;
  /* 不足: patch の base が remote に無い commit だと、その commit までの差分が
   * patch から落ちる一方、マージでは base に入る。 */
  if (!shows.facts.basePushed) return MERGE_DIFF_LOCAL_BASE;
  /* すり替え: 行の対象 PR そのものが入れ替わった(新しい PR が open になった等)。 */
  if (pin.prNumber !== merge.prNumber) return MERGE_DIFF_MISMATCH;
  /* 追い越し: 読んでいる間に push された。 */
  if (pin.headSha !== merge.headSha) return MERGE_STALE_DIFF;
  return null;
}

/* 表示中の patch と対象 PR が同じものを指しているかの照合。空の記録は「判定材料が
 * 無い」であって不一致ではないので、塞ぐ根拠にしない。 */
const SOURCE_CHECKS: ((merge: MergeAffordance, shows: DiffSource) => boolean)[] = [
  /* patch を取ったローカル commit と PR head。branch 名の照合では、別の checkout
   * から PR branch へ push されてローカル worktree が遅れている場合を捕まえられない
   * — 名前はすべて一致したまま、画面に無い commit がマージされる。 */
  (m, s) => !s.facts.commit || !m.headSha || m.headSha === s.facts.commit,
  /* patch は worktree の base branch との差分。retarget は head を 1 commit も
   * 動かさないので、head だけを見ていると素通りする。 */
  (m, s) => !s.base || m.baseRef === s.base,
  /* head branch は worktree のもの(issue 行には fork の closing PR が載りうる)。 */
  (m, s) => !s.branch || m.headRef === s.branch,
  (m, s) => !s.branch || m.headRepo.toLowerCase() === s.repo.toLowerCase(),
];

function sameSource(merge: MergeAffordance, shows: DiffSource): boolean {
  return SOURCE_CHECKS.every((check) => check(merge, shows));
}
