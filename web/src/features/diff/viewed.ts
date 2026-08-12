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

/* FNV-1a を 2 本、別々の乗数で同時に回して 64bit にする。1 パスで両方進めるので
 * 走査コストは 1 本のときと変わらない。
 *
 * 32bit 1 本では狭すぎる。500 files を何度も再取得すれば偶然の衝突が現実的な確率で
 * 起きはじめ、衝突した file は「変わったのに確認済みのまま」になる。
 *
 * ここで守れるのは偶然の衝突までで、**故意に衝突させる相手は守れない**。暗号学的
 * digest ではないので、patch を書く側が両方の版を選べるなら誕生日攻撃で衝突を作れる
 * (連結ハッシュは連結した幅ぶんの強度を持たない)。ブラウザの同期 API に暗号学的
 * digest は無く、`crypto.subtle` は非同期で、patch と同じ render で fingerprint を
 * 出せなくなる。
 * 確認済みは読み進めた位置を覚える道具であって、レビュー回避を防ぐ仕組みではない —
 * その前提はここで明示しておく。 */
const OFFSET_A = 0x811c9dc5; // FNV-1a 32bit の既定値
const PRIME_A = 0x01000193;
const OFFSET_B = 0x9747b28c; // 別系統(MurmurHash2)の種と乗数
const PRIME_B = 0x5bd1e995;

/* 32bit を base36 で表す最大桁。桁を揃えないと 2 本を連結した境界が曖昧になる
 * (`1z` + `41z3` と `1z4` + `1z3` が同じ文字列になる)。 */
const BASE36_WIDTH = 7;

function base36(v: number): string {
  return (v >>> 0).toString(36).padStart(BASE36_WIDTH, "0");
}

/* 長さを先に畳み込んで区切りにする — 入れないと ["ab","c"] と ["a","bc"] が
 * 同じ値になり、行の切れ目の違いを取り違える。 */
function fold(parts: Iterable<string>): string {
  let a = OFFSET_A;
  let b = OFFSET_B;
  for (const s of parts) {
    a = Math.imul(a ^ s.length, PRIME_A);
    b = Math.imul(b ^ s.length, PRIME_B);
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      a = Math.imul(a ^ c, PRIME_A);
      b = Math.imul(b ^ c, PRIME_B);
    }
  }
  return base36(a) + base36(b);
}

/* file の同一性を名乗る部分。
 *
 * mode を混ぜるのは、mode だけの変更が `index` 行も hunk も持たないため。
 * 入れないと chmod +x とその取り消しが同じ値になる。
 * prev 側の object id も要る — rebase や merge-base の更新で最終 blob が同じまま
 * base だけ変わる形があり、new 側だけでは区別できない。 */
function* identityParts(f: FileDiffMetadata): Generator<string> {
  yield f.type;
  yield f.name;
  yield f.prevName ?? "";
  yield f.newObjectId ?? "";
  yield f.prevObjectId ?? "";
  yield f.mode ?? "";
  yield f.prevMode ?? "";
}

/* fingerprint に食わせる材料。可変長のまとまりごとに件数を先に流して、
 * 「hunk が 1 本減って削除行が 1 本増えた」を取り違えないようにする。
 * hunk の位置(`@@` 行)まで含めるのは、同じ置換が file 内の別の箇所へ移った
 * だけでも表示される差分は変わるため。
 * generator なのは、26 万行の file で配列を作り直さないため。 */
function* fingerprintParts(f: FileDiffMetadata): Generator<string> {
  yield* identityParts(f);
  yield String(f.hunks.length);
  for (const h of f.hunks) yield h.hunkSpecs ?? "";
  yield String(f.deletionLines.length);
  yield* f.deletionLines;
  yield String(f.additionLines.length);
  yield* f.additionLines;
}

/* file 1 つの内容 fingerprint。 */
export function fileFingerprint(f: FileDiffMetadata): string {
  return fold(fingerprintParts(f));
}

/* 同じ key に集まった entry が全部同じ file だと言い切れるか。
 *
 * 不正 UTF-8 の byte は正規化で U+FFFD へ潰れるので、`docs/\200.md` と
 * `docs/\201.md` は同じ key になる(`diff.ts` の冒頭を参照)。潰れた跡があるなら
 * identity は失われていて、確認済みは「別の file にもチェックが入って、隠す設定
 * なら本文からも一覧からも消える」= 読んでいない変更が通る、という壊れ方をする。
 * `diff.ts` が変更種別で同じ判定を使っているのと同じ理由だが、こちらは 1 entry でも
 * U+FFFD を含むなら降ろす — アイコンを間違えるより取り違えの害が大きい。
 *
 * 移動元(`prevName`)も同じ目で見る。`isSameFile` は移動先しか見ないが、こちらは
 * `prevName` を fingerprint の材料にしているので、そこが潰れていれば別の rename と
 * 同じ値になる。object id も hunk も持たない pure rename では、材料が名前しか
 * 無いぶんそのまま衝突する(`core.quotePath=false` で移動元だけが不正 UTF-8、
 * という形で起きる)。 */
function sameFileGroup(entries: FileDiffMetadata[]): boolean {
  const first = entries[0];
  if (first === undefined) return false;
  if (entries.some((e) => (e.prevName ?? "").includes("�"))) return false;
  return entries.every((e) => isSameFile(first, e));
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
