import { useCallback, useMemo, useState } from "react";
import { loadViewed, saveViewed } from "./viewedStore";

/* scope ごとの保存値。scope が変われば path の意味も変わるので、両者を組で持つ。 */
type Stored = { scope: string; map: ReadonlyMap<string, string> };

/* 保存済みの fingerprint が現在の patch の fingerprint と一致する path だけを
 * 「いま確認済み」と見なす。これが無効化そのもので、リセット処理は要らない —
 * 内容が変わった file は一致しなくなって自動的に外れ、他の file は残る。 */
function matching(
  stored: ReadonlyMap<string, string>,
  fingerprints: ReadonlyMap<string, string>,
): Set<string> {
  const paths = new Set<string>();
  for (const [path, fp] of stored) {
    if (fingerprints.get(path) === fp) paths.add(path);
  }
  return paths;
}

/* file ごとの「確認済み」。
 *
 * scope は session 行の rowKey。`DiffOverlay` は対象を切り替えても remount されない
 * ので、scope の変化は render 中に見て読み直す — passive effect でのリセットでは
 * `@pierre/diffs` の同期 mount に間に合わない(`useDiffCollapse` と同じ理由)。 */
export function useDiffViewed(scope: string, fingerprints: ReadonlyMap<string, string>) {
  const [stored, setStored] = useState<Stored>(() => ({ scope, map: loadViewed(scope) }));
  /* scope が入れ替わった最初の render はまだ state が古いので、その場で読み直す。
   * storage 読み出しは副作用を持たないので render 中でよい。memo は再パースを
   * 減らすためだけのもので、正しさは再計算しても変わらない。 */
  const loaded = useMemo(() => loadViewed(scope), [scope]);
  const current = stored.scope === scope ? stored.map : loaded;
  const viewedPaths = useMemo(() => matching(current, fingerprints), [current, fingerprints]);

  const setViewed = useCallback(
    (path: string, on: boolean) => {
      const fp = fingerprints.get(path);
      if (fp === undefined) return; // patch に無い path は確認済みにできない
      const map = new Map(current);
      if (on) map.set(path, fp);
      else map.delete(path);
      saveViewed(scope, map);
      setStored({ scope, map });
    },
    [current, fingerprints, scope],
  );

  return { viewedPaths, setViewed };
}
