import { useCallback, useLayoutEffect, useRef } from "react";

/* identity が変わらないイベントハンドラ。中身は毎 render 差し替わるが、返す関数の
 * 参照は mount 中ずっと同じ。
 *
 * memo した子へ渡すハンドラがこれを必要とする。素の useCallback は依存が変わるたび
 * に別の関数になるので、状態を 1 つ変えるだけで memo の shallow 比較が全件外れ、
 * 「触った子だけ描き直す」が成立しない(diff ビュアーでは 500 file 全部の
 * setOptions/render に化ける)。
 *
 * render 中に呼んではいけない。ref の差し替えは commit 後なので、render 中の呼び
 * 出しは 1 つ前の値を見る。 */
export function useStableCallback<A extends unknown[], R>(
  fn: (...args: A) => R,
): (...args: A) => R {
  const ref = useRef(fn);
  useLayoutEffect(() => {
    ref.current = fn;
  });
  return useCallback((...args: A) => ref.current(...args), []);
}
