import type { FileDiffMetadata } from "@pierre/diffs";
import { isSameFile, unquoteGitPath } from "./diff";

/* 「確認済み」の identity。
 *
 * GitHub の Viewed は blob が変われば外れる。`/api/diff` の `files[]` は path しか
 * identity を持たない(sha も変更種別も返さない — 1 MiB 応答上限へのバイト圧と
 * `--name-status` の追加呼び出しが要るため)ので、内容の同一性は patch の
 * パース結果から作る。
 *
 * `newObjectId`(patch の `index` 行)単独では足りない。pure rename は
 * `similarity index 100%` + `rename from/to` だけで `index` 行を持たず undefined に
 * なり、mode だけの変更も同様。そこで描画対象の行そのものを畳み込む。
 * コストは応答の 1 MiB 上限で有界(約 100 万文字を 1 パス)で、patch をキーにした
 * memo の中で 1 回だけ走る。 */

const FNV_OFFSET = 0x811c9dc5;
const FNV_PRIME = 0x01000193;

/* FNV-1a 32bit の 1 項目ぶん。長さを先に畳み込んで区切りにする — 入れないと
 * ["ab","c"] と ["a","bc"] が同じ値になり、行の切れ目の違いを取り違える。 */
function foldString(hash: number, s: string): number {
  let h = Math.imul(hash ^ s.length, FNV_PRIME);
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), FNV_PRIME);
  return h;
}

function foldLines(hash: number, lines: string[]): number {
  let h = foldString(hash, String(lines.length));
  for (const line of lines) h = foldString(h, line);
  return h;
}

/* file 1 つの内容 fingerprint。衝突しても失われるのは「変わったのに確認済みが
 * 残る」ことだけで、同じ file の旧内容と新内容が 32bit で衝突する確率の話。 */
export function fileFingerprint(f: FileDiffMetadata): string {
  /* mode を混ぜるのは、mode だけの変更が `index` 行も hunk も持たないため。
   * 入れないと chmod +x とその取り消しが同じ値になり、確認済みが残ったまま
   * 「隠す」で二度と出てこない。 */
  const meta = [
    f.type,
    f.name,
    f.prevName ?? "",
    f.newObjectId ?? "",
    f.mode ?? "",
    f.prevMode ?? "",
  ];
  let h = foldLines(FNV_OFFSET, meta);
  h = foldLines(h, f.deletionLines);
  h = foldLines(h, f.additionLines);
  return (h >>> 0).toString(36);
}

/* 同じ key に集まった entry が全部同じ file だと言い切れるか。
 *
 * 不正 UTF-8 の byte は正規化で U+FFFD へ潰れるので、`docs/\200.md` と
 * `docs/\201.md` は同じ key になる(`diff.ts` の冒頭を参照)。潰れた跡があるなら
 * identity は失われていて、確認済みは「別の file にもチェックが入って、隠す設定
 * なら本文からも一覧からも消える」= 読んでいない変更が通る、という壊れ方をする。
 * `diff.ts` が変更種別で同じ判定を使っているのと同じ理由だが、こちらは 1 entry でも
 * U+FFFD を含むなら降ろす — アイコンを間違えるより取り違えの害が大きい。 */
function sameFileGroup(entries: FileDiffMetadata[]): boolean {
  const first = entries[0];
  return first !== undefined && entries.every((e) => isSameFile(first, e));
}

/* 正規化 path -> fingerprint。key を生のパスへ戻すのはサイドバーが
 * `files[].path` で引くため(`indexDiffFilesByPath` と同じ規則)。
 * file type change の同 path 2 entry は 1 つの file なので、両方を畳んで
 * 1 エントリにする — 確認済みも 1 つとして扱う。
 * 同一と言い切れない key は fingerprint を持たせない。呼び出し側はこの Map に
 * 無い path をチェック不可として扱う(確認済みにできず、復元もされない)。 */
export function indexFingerprintsByPath(files: FileDiffMetadata[]): Map<string, string> {
  const groups = new Map<string, FileDiffMetadata[]>();
  for (const f of files) {
    const path = unquoteGitPath(f.name);
    const at = groups.get(path);
    if (at) at.push(f);
    else groups.set(path, [f]);
  }
  const fingerprints = new Map<string, string>();
  for (const [path, entries] of groups) {
    if (sameFileGroup(entries)) fingerprints.set(path, entries.map(fileFingerprint).join("."));
  }
  return fingerprints;
}

/* 正規化した path の index 順の並び。本文側は plan の index で file を指すので、
 * index -> path の対応がここで要る(サイドバーは path で引く)。 */
export function diffFilePaths(files: FileDiffMetadata[]): string[] {
  return files.map((f) => unquoteGitPath(f.name));
}

/* path の集合を本文側の plan index の集合へ。確認済み(viewedPaths)と、そもそも
 * チェックできる file(fingerprints の key)の両方をこれで引く。 */
export function indicesForPaths(
  paths: string[],
  member: { has(path: string): boolean },
): Set<number> {
  const indices = new Set<number>();
  paths.forEach((path, i) => {
    if (member.has(path)) indices.add(i);
  });
  return indices;
}
