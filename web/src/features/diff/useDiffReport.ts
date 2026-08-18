import { useCallback, useMemo, useRef, useState } from "react";
import type { DiffResponse } from "../../transport/types";

/* diff が返した patch の素性。マージボタンはこれと対象 PR を照合する。
 *
 * snapshot 側の値(2 秒周期)ではなくこちらを使うのが要点 — 画面に出ている
 * patch そのものの性質でなければ、照合しても「今の worktree」の話にしかならない。 */
export interface DiffFacts {
  /* 応答が手元にあるか。初回ロード中・再取得中・取得失敗では false で、その間は
   * patch について何も言えない。 */
  known: boolean;
  commit: string;
  dirty: boolean;
  basePushed: boolean;
}

/* 取得できていない間は、いちばん厳しい側に倒す。 */
const NO_DIFF: DiffFacts = { known: false, commit: "", dirty: true, basePushed: false };

/* diff オーバーレイが親へ返すもの。
 *
 * covering は隠れた peek のポーリングを止めるため、facts は行のマージボタンの
 * 照合材料。どちらもオーバーレイの中でしか分からないので、state はここに置いて
 * setter を降ろす。 */
export interface DiffReport {
  covering: boolean;
  /* useMergeFlow に渡す形。開いている行と、その patch の素性。 */
  shown: { key: string | null; facts: DiffFacts };
  setCovering: (covering: boolean) => void;
  setDiff: (diff: DiffResponse | null) => void;
}

export function useDiffReport(key: string | null): DiffReport {
  const [covering, setCovering] = useState(false);
  /* 応答そのものを、どの行のものかと一緒に持つ。facts は導出する — setter を
   * 包むと render ごとに identity が変わり、報告する側の effect が毎 render 走って
   * state を書き換え続ける。 */
  const [shown, setShown] = useState<{ key: string | null; diff: DiffResponse | null }>({
    key: null,
    diff: null,
  });
  /* 行が変わった瞬間に、まだ前の行の応答しか無い render が挟まる。setDiff を呼ぶ
   * のは effect なので 1 コミット遅く、その間に前の行の facts を新しい行の
   * branch/base と組み合わせると、両者が同じ commit を指していれば照合を全部
   * 通ってしまう。key を持たせて、その render で同期的に捨てる。 */
  const keyRef = useRef(key);
  keyRef.current = key;
  const setDiff = useCallback((diff: DiffResponse | null) => {
    setShown({ key: keyRef.current, diff });
  }, []);
  const facts = useMemo(
    () => (shown.key === key && shown.diff ? factsOf(shown.diff) : NO_DIFF),
    [shown, key],
  );
  return { covering, shown: { key, facts }, setCovering, setDiff };
}

function factsOf(diff: DiffResponse): DiffFacts {
  return {
    known: true,
    commit: diff.headCommit ?? "",
    /* 欠落は「不明」。塞ぐ側に倒す — 旧いサーバの応答で照合が緩むと、
     * 新しい遮断が黙って無効になる。 */
    dirty: diff.dirty ?? true,
    basePushed: diff.basePushed ?? false,
  };
}
