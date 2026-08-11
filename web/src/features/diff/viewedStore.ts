import { localKeysWithPrefix, readLocal, writeLocal } from "../../shared/localStore";

/* 確認済みの永続化。dashboard サーバーは GET-only で mutation endpoint を持たない
 * (CLAUDE.md)ので、保存先は localStorage しかない。
 *
 * キーは scope(session 行の rowKey)ごとに 1 本にする。全 scope を 1 本の JSON に
 * まとめると、チェック 1 個ごとに全 scope 分を再シリアライズすることになる
 * (contract 上限は 1 scope あたり 500 files)。
 *
 * 保存値は敵性入力。別タブ・拡張・手で書き換えられるので、型も件数も信用せずに
 * 落とす(先例: features/settings/diffThemes.ts の normalizeDiffTheme)。 */

const PREFIX = "fanout.diffViewed.";
/* `/api/diff` の contract 上限と同じ。1 scope が持ちうる file 数の天井。 */
const MAX_FILES = 500;
/* 残す scope の本数。超えたぶんは最終更新の古い順に捨てる。 */
const MAX_SCOPES = 8;

/* 保存形。`t` は剪定の順序付けだけに使う。 */
interface ViewedRecord {
  v: 1;
  t: number;
  files: Record<string, string>;
}

function keyFor(scope: string): string {
  return PREFIX + scope;
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function isRecordShape(body: unknown): body is ViewedRecord {
  if (!isPlainObject(body)) return false;
  return body.v === 1 && typeof body.t === "number" && isPlainObject(body.files);
}

function parseRecord(raw: string | null): ViewedRecord | null {
  if (raw === null) return null;
  try {
    const body: unknown = JSON.parse(raw);
    return isRecordShape(body) ? body : null;
  } catch {
    return null; // JSON ですらない
  }
}

/* string 同士のペアだけを採り、上限で切る。 */
function toFileMap(files: Record<string, string>): Map<string, string> {
  const pairs = Object.entries(files).filter(([path, fp]) => path !== "" && typeof fp === "string");
  return new Map(pairs.slice(0, MAX_FILES));
}

/* path -> fingerprint。 */
export function loadViewed(scope: string): Map<string, string> {
  const rec = parseRecord(readLocal(keyFor(scope)));
  return rec ? toFileMap(rec.files) : new Map();
}

/* 空になったら scope ごと消す(「既定値ならキーを消す」既存規約)。 */
export function saveViewed(scope: string, files: ReadonlyMap<string, string>) {
  const key = keyFor(scope);
  if (files.size === 0) {
    writeLocal(key, null);
    return;
  }
  const rec: ViewedRecord = { v: 1, t: Date.now(), files: Object.fromEntries(files) };
  writeLocal(key, JSON.stringify(rec));
  pruneScopes(key);
}

/* 書いた直後に本数を見て、溢れたぶんを最終更新の古い順に捨てる。いま書いた
 * scope は `t` が最新なので残る。読めない値は `t: 0` 扱いで先に捨てる。 */
function pruneScopes(keepKey: string) {
  const keys = localKeysWithPrefix(PREFIX);
  if (keys.length <= MAX_SCOPES) return;
  const ordered = keys
    .map((key) => ({ key, t: parseRecord(readLocal(key))?.t ?? 0 }))
    .sort((a, b) => a.t - b.t);
  for (const { key } of ordered.slice(0, keys.length - MAX_SCOPES)) {
    if (key !== keepKey) writeLocal(key, null);
  }
}
