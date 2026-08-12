import { useCallback, useMemo, useSyncExternalStore } from "react";
import {
  parseViewed,
  readViewedRaw,
  setViewedEntry,
  subscribeViewed,
  type ViewedEntry,
} from "./viewedStore";

/* 保存済みの entry のうち、現在の patch の fingerprint と一致するものを
 * 「いま確認済み」と見なす。これが無効化そのもので、リセット処理は要らない —
 * 内容が変わった file は一致しなくなって自動的に外れ、他の file は残る。
 *
 * 一致しなくなった entry は消さずに残す。消すと、変更を戻して同じ内容に帰った
 * file の確認済みが二度と復活しない(fingerprint は内容が同じなら同じ値になる)。
 * 際限なく伸びないよう、保存側が path ごと・scope ごとの上限で切る。 */
function matching(stored: ViewedEntry[], fingerprints: ReadonlyMap<string, string>): Set<string> {
  const paths = new Set<string>();
  for (const [path, fp] of stored) {
    if (fingerprints.get(path) === fp) paths.add(path);
  }
  return paths;
}

/* file ごとの「確認済み」。
 *
 * scope は session 行の rowKey。状態は storage が唯一の正で、ローカルに複製しない
 * — `DiffOverlay` は対象を切り替えても remount されないので、複製すると scope の
 * 入れ替わりと別タブの書き込みの両方を自前で無効化して回ることになる。
 * 購読は store 側が自分の書き込みと `storage` イベントを束ねて 1 本で配る。 */
export function useDiffViewed(scope: string, fingerprints: ReadonlyMap<string, string>) {
  const raw = useSyncExternalStore(
    subscribeViewed,
    () => readViewedRaw(scope),
    () => null, // SSR は無い(dashboard は CSR のみ)。念のため空で揃える
  );
  const stored = useMemo(() => parseViewed(raw), [raw]);
  const viewedPaths = useMemo(() => matching(stored, fingerprints), [stored, fingerprints]);

  /* 書き戻すのは、いま画面に出ている内容の entry 1 件だけ。一覧全体を渡したり
   * path 単位で消したりすると、別タブが持っている別の内容のチェックを巻き戻す
   * (viewedStore の setViewedEntry を参照)。 */
  const setViewed = useCallback(
    (path: string, on: boolean) => {
      const fp = fingerprints.get(path);
      if (fp === undefined) return; // patch に無い / identity が曖昧な path は扱わない
      setViewedEntry({ scope, entry: [path, fp], viewed: on });
    },
    [fingerprints, scope],
  );

  return { viewedPaths, setViewed };
}
