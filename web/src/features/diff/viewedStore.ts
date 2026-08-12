import { localKeysWithPrefix, readLocal, writeLocal } from "../../shared/localStore";

/* 確認済みの永続化。dashboard サーバーは GET-only で mutation endpoint を持たない
 * (CLAUDE.md)ので、保存先は localStorage しかない。
 *
 * キーは scope(session 行の rowKey)ごとに 1 本にする。全 scope を 1 本の JSON に
 * まとめると、チェック 1 個ごとに全 scope 分を再シリアライズすることになる
 * (contract 上限は 1 scope あたり 500 files)。
 *
 * localStorage は origin(ポート込み)ごとなので、`fanout dashboard --web` を
 * 既定の OS 任せポートで起動し直すと丸ごと引き継がれない。これは表示モードや
 * パネル幅と同じ制約で、持ち越したいときは `--port N` を固定する。
 *
 * 保存値は敵性入力。別タブ・拡張・手で書き換えられるので、型も件数も信用せずに
 * 落とす(先例: features/settings/diffThemes.ts の normalizeDiffTheme)。 */

const PREFIX = "fanout.diffViewed.";
/* `/api/diff` の contract 上限と同じ。1 scope が持ちうる file 数の天井。 */
const MAX_FILES = 500;
/* 残す scope の本数。fan-out は親 1 つに子が何十個も並ぶので、数個で切ると
 * 「隣の子をレビューしている間に前の子の確認済みが消える」が普通に起きる。 */
const MAX_SCOPES = 50;

/* 保存形。`t` は scope の剪定順、`files` の並びは file の剪定順に使う。
 *
 * `files` は object にしない。JS は整数に見える key(`"1"`)を挿入順より先に、
 * 昇順で列挙するので、リポジトリ直下に `1` という file があるだけで並びが崩れる —
 * いちばん新しいチェックが先頭へ回り、500 件上限の切り捨てで真っ先に落ちる。
 * 並びに意味を持たせるなら配列で持つ。 */
interface ViewedRecord {
  v: 2;
  t: number;
  files: [path: string, fingerprint: string][];
}

function viewedStorageKey(scope: string): string {
  return PREFIX + scope;
}

/* 保存単位。`rowKey` だけでは足りない — localStorage は origin ごとなので、同じ
 * `--port N` を固定して別のリポジトリの dashboard を順に開くと `142#101` のような
 * rowKey がそのまま衝突する。
 *
 * 前置きに使うのは `repo`(owner/name)ではなく `projectRoot`。repo は
 * `gh repo view` の解決待ちで最初の snapshot が "" を配り、あとから埋まる — その間に
 * 付けたチェックが別のキーへ書かれて迷子になる。projectRoot はサーバー起動前に
 * 決まっていて動かず、gh 未ログインでもリポジトリを区別できる。
 * どちらも自由文字列なので、連結する前に両方をエンコードする。 */
export function viewedScope(projectRoot: string, rowKey: string): string {
  return `${encodeURIComponent(projectRoot)}/${encodeURIComponent(rowKey)}`;
}

/* 購読は 2 系統ある。別タブの書き込み(`storage` イベント)と、自分の書き込み。
 * `storage` は書いた本人には飛ばないので、両方をここで 1 つの通知に束ねる
 * (features/settings/useSettings.ts と同じ module store の作法)。
 * これが無いと、片方しか反映されない購読側を書くことになる。 */
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function onStorage(e: StorageEvent) {
  // key === null は clear()。確認済みのキーに関係する変化だけ拾う
  if (e.storageArea !== localStorage) return;
  if (e.key === null || e.key.startsWith(PREFIX)) emit();
}

export function subscribeViewed(onChange: () => void): () => void {
  if (listeners.size === 0) window.addEventListener("storage", onStorage);
  listeners.add(onChange);
  return () => {
    listeners.delete(onChange);
    if (listeners.size === 0) window.removeEventListener("storage", onStorage);
  };
}

/* 生の保存文字列。`useSyncExternalStore` の snapshot はこれ(同じ内容なら同じ
 * 文字列が返るので、キャッシュ要件を満たす)。 */
export function readViewedRaw(scope: string): string | null {
  return readLocal(viewedStorageKey(scope));
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/* `t` は有限数だけ受ける。`1e999` は JSON.parse が Infinity にするので
 * `typeof === "number"` を通り、剪定の比較関数が NaN を返して並び順が壊れる。 */
function isRecordShape(body: unknown): body is ViewedRecord {
  if (!isPlainObject(body)) return false;
  return body.v === 2 && Number.isFinite(body.t) && Array.isArray(body.files);
}

/* 2 要素の string 配列だけを採る(要素の中身も敵性入力)。 */
function isPair(v: unknown): v is [string, string] {
  return Array.isArray(v) && v.length === 2 && typeof v[0] === "string" && typeof v[1] === "string";
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

/* 形の合うペアだけを採り、上限で切る。切るのは末尾ではなく先頭側 — 配列の末尾が
 * いちばん新しいチェックなので、先頭を落とせば「上限に達した瞬間から新しい
 * チェックが保存されない」を避けられる。 */
function toFileMap(files: unknown[]): Map<string, string> {
  const pairs = files.filter(isPair).filter(([path]) => path !== "");
  return new Map(pairs.slice(-MAX_FILES));
}

/* 生の保存文字列から path -> fingerprint へ。文字列を受け取るのは、購読側が
 * `useSyncExternalStore` の snapshot(= 生の文字列)を持つため。 */
export function parseViewed(raw: string | null): Map<string, string> {
  const rec = parseRecord(raw);
  return rec ? toFileMap(rec.files) : new Map();
}

export function loadViewed(scope: string): Map<string, string> {
  return parseViewed(readLocal(viewedStorageKey(scope)));
}

/* 1 file 分だけを書き換える。`fp` が null なら確認済みを外す。
 *
 * 呼び出し側の Map をそのまま書き戻さないこと。別タブが直前に書いた分が
 * こちらの render 時点の snapshot には無く、丸ごと上書きすると消える
 * (`storage` イベントの到着はこちらの操作より遅れうる)。書く直前に読み直して
 * 1 件だけ足し引きすれば、少なくとも「相手のチェックを巻き戻す」ことは無い。
 *
 * 既存 path は delete してから set する。`Map.set` は既存 key の位置を変えないので、
 * 入れ直さないと「いちばん新しいチェック」が上限で落ちる側に残ってしまう。 */
export function setViewedPath(scope: string, path: string, fp: string | null) {
  const key = viewedStorageKey(scope);
  const files = parseViewed(readLocal(key));
  files.delete(path);
  if (fp !== null) files.set(path, fp);
  if (files.size === 0) writeLocal(key, null);
  else writeRecord(key, files);
  emit();
}

function writeRecord(key: string, files: ReadonlyMap<string, string>) {
  const rec: ViewedRecord = { v: 2, t: Date.now(), files: [...files].slice(-MAX_FILES) };
  const body = JSON.stringify(rec);
  /* 先に剪定してから書く。逆にすると、quota で書けなかった直後に領域を空けて
   * 終わり(誰も書き直さない)になり、古い scope を捨てただけで終わる。 */
  pruneScopes(key);
  if (writeLocal(key, body)) return;
  /* それでも入らないなら、いちばん古い scope を 1 本落として一度だけ retry する */
  dropOldestScope(key);
  writeLocal(key, body);
}

/* 最終更新の古い順。読めない値は `t: 0` 扱いで先に捨てる。 */
function scopesByAge(): { key: string; t: number }[] {
  return localKeysWithPrefix(PREFIX)
    .map((key) => ({ key, t: parseRecord(readLocal(key))?.t ?? 0 }))
    .sort((a, b) => a.t - b.t);
}

/* 本数が溢れていたら古い順に捨てる。書き込み前に呼ぶので、いま書く scope を
 * 足した後の本数で数える。溢れていないときに早期 return するのを忘れないこと —
 * `slice(0, 負数)` は「末尾から数えた手前まで」= ほぼ全部、になる。 */
function pruneScopes(keepKey: string) {
  const others = scopesByAge().filter(({ key }) => key !== keepKey);
  const excess = others.length + 1 - MAX_SCOPES;
  if (excess <= 0) return;
  for (const { key } of others.slice(0, excess)) writeLocal(key, null);
}

function dropOldestScope(keepKey: string) {
  const oldest = scopesByAge().find(({ key }) => key !== keepKey);
  if (oldest) writeLocal(oldest.key, null);
}
