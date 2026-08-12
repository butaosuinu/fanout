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
/* `/api/diff` の contract 上限と同じ。1 scope が持ちうる **path** の天井。
 * entry 数で切ってはいけない — 同じ path の古い fingerprint(履歴)が現行 file の
 * 枠を食い、500 file 全部を確認済みにしたあとに 1 file が変わって再チェックされる
 * だけで、無関係な未変更 file のチェックが落ちる。 */
const MAX_FILES = 500;
/* 1 つの path が持てる fingerprint の数。内容が行き来する file(生成物の
 * 再生成など)1 つに 500 件の枠を食われないための蓋。 */
const MAX_PER_PATH = 4;
/* 残す scope の本数。fan-out は親 1 つに子が何十個も並ぶので、数個で切ると
 * 「隣の子をレビューしている間に前の子の確認済みが消える」が普通に起きる。 */
const MAX_SCOPES = 50;

/* 確認済み 1 件 = 「この path のこの内容を見た」。同じ path が複数の fingerprint を
 * 持ちうる。1 path 1 fingerprint にすると、2 タブで同じ行を開いて片方だけ再取得した
 * とき、古い patch を見ているタブの操作が新しいほうのチェックを消す(どちらの
 * 「見た」も本当なので、片方を捨てる理由が無い)。 */
export type ViewedEntry = [path: string, fingerprint: string];

/* 保存形。`t` は scope の剪定順、`files` の並びは entry の剪定順に使う。
 *
 * `files` は object にしない。JS は整数に見える key(`"1"`)を挿入順より先に、
 * 昇順で列挙するので、リポジトリ直下に `1` という file があるだけで並びが崩れる —
 * いちばん新しいチェックが先頭へ回り、500 件上限の切り捨てで真っ先に落ちる。
 * 同じ path を複数持てる必要もあるので、いずれにせよ配列。 */
interface ViewedRecord {
  v: 2;
  t: number;
  files: ViewedEntry[];
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

/* 形の合うペアだけを採り、上限で切る。 */
export function parseViewed(raw: string | null): ViewedEntry[] {
  const rec = parseRecord(raw);
  if (!rec) return [];
  return capEntries(rec.files.filter(isPair).filter(([path]) => path !== ""));
}

export function loadViewed(scope: string): ViewedEntry[] {
  return parseViewed(readLocal(viewedStorageKey(scope)));
}

/* 1 entry(path と fingerprint の組)だけを書き換える。
 *
 * 呼び出し側が持っている一覧をそのまま書き戻さないこと。別タブが直前に書いた分が
 * こちらの render 時点の snapshot には無く、丸ごと上書きすると消える
 * (`storage` イベントの到着はこちらの操作より遅れうる)。書く直前に読み直して
 * 1 件だけ足し引きすれば、相手のチェックを巻き戻すことは無い。
 *
 * 外すときも fingerprint 単位で消す。path 単位で消すと、片方のタブだけ再取得した
 * 状態で古い patch のタブが操作したときに、新しい内容のチェックまで巻き添えになる。
 *
 * 同じ組を入れ直すときは末尾へ動かす。並びがそのまま剪定順なので、動かさないと
 * 「いちばん新しいチェック」が先に落ちる側に残る。 */
export function setViewedEntry({
  scope,
  entry,
  viewed,
}: {
  scope: string;
  entry: ViewedEntry;
  viewed: boolean;
}) {
  const key = viewedStorageKey(scope);
  const [path, fp] = entry;
  const rest = parseViewed(readLocal(key)).filter(([p, f]) => p !== path || f !== fp);
  const next = viewed ? [...rest, entry] : rest;
  if (next.length === 0) writeLocal(key, null);
  else writeRecord(key, capEntries(next));
  emit();
}

/* 上限は 2 段。まず 1 path が持てる fingerprint を絞り、そのうえで path の本数を
 * 絞る。この順序に意味がある — 逆にすると履歴が path の枠を数えてしまう。
 * どちらも「古いほうから落とす」で、配列の並びがそのまま剪定順。 */
function capEntries(entries: ViewedEntry[]): ViewedEntry[] {
  return capPaths(capPerPath(entries));
}

/* 1 path が持てる fingerprint を新しいほうから MAX_PER_PATH 件に絞る。内容が
 * 行き来する file 1 つで枠を食い潰させない。 */
function capPerPath(entries: ViewedEntry[]): ViewedEntry[] {
  const total = new Map<string, number>();
  for (const [path] of entries) total.set(path, (total.get(path) ?? 0) + 1);
  /* 前から数えて、その path の超過分(= 古いほう)だけを落とす。 */
  const seen = new Map<string, number>();
  return entries.filter(([path]) => {
    const n = (seen.get(path) ?? 0) + 1;
    seen.set(path, n);
    return n > (total.get(path) ?? 0) - MAX_PER_PATH;
  });
}

/* path の本数を新しいほうから MAX_FILES に絞る。落とすのは path 単位なので、
 * ある path の履歴が別の path の現行 entry を押し出すことはない。
 * 新しさは「その path が最後に出てくる位置」で測る。 */
function capPaths(entries: ViewedEntry[]): ViewedEntry[] {
  const lastAt = new Map<string, number>();
  entries.forEach(([path], i) => lastAt.set(path, i));
  if (lastAt.size <= MAX_FILES) return entries;
  const keep = new Set(
    [...lastAt]
      .sort((a, b) => a[1] - b[1])
      .slice(-MAX_FILES)
      .map(([path]) => path),
  );
  return entries.filter(([path]) => keep.has(path));
}

function writeRecord(key: string, files: ViewedEntry[]) {
  const rec: ViewedRecord = { v: 2, t: Date.now(), files };
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
