import { parsePatchFiles, type FileDiffMetadata } from "@pierre/diffs";
import type { DiffFileEntry, DiffOmittedReason, DiffResponse } from "./types";
import { clock } from "./format";

/* /api/diff の連結 patch を file 単位のパース済み metadata へ。分割・解釈は
 * ライブラリ自身のパーサに委譲する(自前の分割規則を持つと lib 更新で drift
 * する)。file type change は同 path の deleted/new 2 block = 2 entry になる。 */
export function parseDiffFiles(patch: string): FileDiffMetadata[] {
  if (!patch) return [];
  return parsePatchFiles(patch).flatMap((p) => p.files);
}

/* worktree 由来の patch は敵性入力で、サーバーの byte 上限(1 MiB 応答)は DOM
 * コストを制限しない。契約内でも次を作れる:
 *   (a) 改行だけの 256 KiB ファイル → 約 26 万行
 *   (b) 交互トークン(`a+1+`…)の高密度行 → Shiki が 1 文字 1 span まで出す
 *   (c) 399 文字の置換行 → 行内 word 差分が decoration span を大量に生む
 * これを DOM node の総量予算で受けると、正当な PR サイズ(20 file × 200 行)でも
 * 予算が数 file で尽きて残りが折りたたまれる。そこで予算ではなく仮想化で受ける:
 * DiffOverlay は file 列を @pierre/diffs の <Virtualizer> で包み、画面外の file は
 * 高さだけ確保した placeholder(shadow root + div 1 個)になる。描画 node 数は
 * patch のサイズではなくビューポートに比例するので、(a) も 500 files も有界。
 *
 * ここに残る 3 つの閾値は、仮想化でも有界にならない「file 単位で一度に走る処理」
 * だけを抑えるためのもの:
 *   - 折りたたみ    : 1 file が長すぎると一覧性を失う(描画コストの話ではない)
 *   - highlight     : Shiki のトークン化は描画範囲ではなく file 全体に走る
 *   - inline diff   : 行内 word 差分の計算も同じく file 全体に走る
 * 行の内容量は行数では測れない((b) は 66 行で 65,000 文字)ため、後ろ 2 つは
 * 描画対象の総文字数で判定する。 */

/* これ以上の描画行数を持つ file だけ初期状態で折りたたむ。それ未満は展開して出す。 */
export const COLLAPSE_LINE_THRESHOLD = 1_000;

/* Shiki のトークン化を諦めて plaintext で描画する閾値。tokenizeMaxLength に
 * 0 を渡すと isDiffMassive が必ず成立して plaintext 経路に落ちる。
 *
 * 文字数と行数の両方で掛ける。トークン化は描画範囲ではなく file 全体に走るので、
 * 短い行が大量にある file は文字数だけでは止まらない: 74,000 行の `x` は
 * 74,000 文字で下の文字数上限を通り、ライブラリ自身の tokenizeMaxLength
 * (既定 100,000、比較対象は行数)にも掛からないため、展開した瞬間に main
 * thread で 74,000 行ぶんのトークン化と HAST 構築が走る。契約(1 file 256 KiB)
 * 内で作れる形なので行数側の蓋が要る。 */
export const HIGHLIGHT_MAX_CHARS = 150_000;
export const HIGHLIGHT_MAX_LINES = 20_000;
export const TOKENIZE_MAX_LENGTH_PLAIN = 0;

/* 行内 word 差分を切る総文字数。ライブラリ既定の `word-alt` は plaintext 描画でも
 * 残り、ライブラリ自身が自動停止するのは 1,000 行超のときだけなので、行数の
 * 少ない高密度 patch は素通りする(実測: 500 行 × 399 文字の置換 2 side で
 * 6,065ms → `none` で 287ms、decoration 1,500 → 0)。 */
export const INLINE_DIFF_MAX_CHARS = 30_000;
export const LINE_DIFF_TYPE_PLAIN = "none";

export const TOKENIZE_MAX_LINE_LENGTH = 400;

/* hunk 単位の描画行数の合計。file 単位の unifiedLineCount は hunk 前の
 * 折りたたみ済み context(collapsedBefore)を含む絶対位置ベースのため
 * 使わない — 6,000 行目の 1 行変更が 6,001 行と数えられ、正当な小さい
 * diff を誤って畳む。 */
export function renderedLineCount(f: FileDiffMetadata): number {
  let n = 0;
  for (const h of f.hunks) n += h.unifiedLineCount;
  return n;
}

/* 描画される行テキストの総文字数。Shiki token(≒ DOM 要素)数は行数では
 * なく内容量に比例するため、行数予算と別に拘束する。patch 由来の diff では
 * additionLines / deletionLines が描画対象行そのもの(context は両側に
 * 現れて二重に数えるが、予算として保守的な側に倒れるだけ)。 */
export function renderedCharCount(f: FileDiffMetadata): number {
  let n = 0;
  for (const s of f.deletionLines) n += s.length;
  for (const s of f.additionLines) n += s.length;
  return n;
}

export interface DiffFilePlan {
  file: FileDiffMetadata;
  lines: number;
  chars: number;
  /* 初期状態で折りたたむ(ユーザーの展開操作で上書きされる) */
  initiallyCollapsed: boolean;
  /* Shiki の syntax highlight を有効にできる */
  highlight: boolean;
  /* 行内 word 差分を有効にできる */
  inlineDiff: boolean;
}

/* 各 file の描画方針。file 同士は独立で、走る合計予算は持たない
 * (合計の有界性は仮想化が担う — 冒頭のコメントを参照)。 */
export function planDiffFiles(files: FileDiffMetadata[]): DiffFilePlan[] {
  return files.map((file) => {
    const lines = renderedLineCount(file);
    const chars = renderedCharCount(file);
    return {
      file,
      lines,
      chars,
      initiallyCollapsed: lines >= COLLAPSE_LINE_THRESHOLD,
      highlight: chars <= HIGHLIGHT_MAX_CHARS && lines <= HIGHLIGHT_MAX_LINES,
      inlineDiff: chars <= INLINE_DIFF_MAX_CHARS,
    };
  });
}

export const OMITTED_REASON_LABELS: Record<Exclude<DiffOmittedReason, "">, string> = {
  binary: "バイナリのため patch なし",
  tooLarge: "サイズ上限超過のため patch なし",
  collectionLimit: "収集上限(10 MiB)で省略",
  responseLimit: "応答上限(1 MiB)で省略",
};

export interface DiffFileGroup {
  /* 末尾 "/" 付きのディレクトリ。リポジトリ直下は "" */
  dir: string;
  files: DiffFileEntry[];
}

export function fileDir(path: string): string {
  const at = path.lastIndexOf("/");
  return at < 0 ? "" : path.slice(0, at + 1);
}

export function fileBase(path: string): string {
  const at = path.lastIndexOf("/");
  return at < 0 ? path : path.slice(at + 1);
}

/* サイドバー用に同じディレクトリの file をまとめる。path の byte 順では
 * `a/b.ts` < `a/c/d.ts` < `a/e.ts` のように同一ディレクトリが連続しないので、
 * 連続塊ではなく Map で束ねる(グループの並びは初出順)。 */
export function groupDiffFilesByDir(files: DiffFileEntry[]): DiffFileGroup[] {
  const groups = new Map<string, DiffFileEntry[]>();
  for (const f of files) {
    const dir = fileDir(f.path);
    const at = groups.get(dir);
    if (at) at.push(f);
    else groups.set(dir, [f]);
  }
  return [...groups].map(([dir, entries]) => ({ dir, files: entries }));
}

/* patch 中の path を生のパスへ戻す。git は core.quotePath(既定 on)のとき、
 * 非 ASCII や制御文字を含む path を `"..."` で囲んで C 形式にエスケープして
 * 出すため、patch から得た name はサーバーの files[].path と一致しない。
 * 非 ASCII は UTF-8 の各バイトが 8 進エスケープになるので、バイト列へ戻して
 * から decode する。
 *
 * 引用符の有無で分岐しないこと。parsePatchFiles は外側の `"` を剥がすが
 * エスケープはそのまま残す(実測: `"docs/\346..."` → `docs/\346...`)。
 * 代わりに「全ての `\` が正しいエスケープの開始である」ことを条件にし、
 * 1 つでも解釈できなければ元の name を返す(リテラルの `\` を含む実在の
 * ファイル名を壊さないため)。 */
export function unquoteGitPath(name: string): string {
  const body =
    name.length >= 2 && name.startsWith('"') && name.endsWith('"') ? name.slice(1, -1) : name;
  if (!body.includes("\\")) return body === name ? name : body;
  const encoder = new TextEncoder();
  const bytes: number[] = [];
  const simple: Record<string, number> = {
    a: 7,
    b: 8,
    f: 12,
    n: 10,
    r: 13,
    t: 9,
    v: 11,
    '"': 34,
    "\\": 92,
  };
  for (let i = 0; i < body.length; i++) {
    const c = body[i]!;
    if (c !== "\\") {
      bytes.push(...encoder.encode(c));
      continue;
    }
    const next = body[++i];
    if (next === undefined) return name; // 末尾が単独の \ = 想定外の形。触らない
    if (next >= "0" && next <= "7") {
      const oct = body.slice(i, i + 3);
      if (!/^[0-7]{3}$/.test(oct)) return name;
      bytes.push(parseInt(oct, 8));
      i += 2;
      continue;
    }
    const mapped = simple[next];
    if (mapped === undefined) return name; // 知らないエスケープ。触らない
    bytes.push(mapped);
  }
  return decodeUtf8LikeGo(bytes);
}

/* UTF-8 の開始 byte から列の長さ。不正な開始 byte は 0。 */
function utf8SequenceLength(b: number): number {
  if (b <= 0x7f) return 1;
  if (b >= 0xc2 && b <= 0xdf) return 2;
  if (b >= 0xe0 && b <= 0xef) return 3;
  if (b >= 0xf0 && b <= 0xf4) return 4;
  return 0;
}

/* 不正な UTF-8 の置換規則をサーバー(Go)に合わせる。
 *
 * Go の encoding/json は不正な byte を 1 個ずつ U+FFFD にするが、WHATWG の
 * TextDecoder は不正な列をまとめて 1 個の U+FFFD にする(例: `\341\200` は
 * Go が 2 個、TextDecoder が 1 個)。素の TextDecoder で復号すると、path に
 * 不正 byte を含む file だけ files[].path と key が食い違い、サイドバーから
 * 飛べなくなる。列単位で試し、駄目なら 1 byte 進めて U+FFFD を置く。 */
function decodeUtf8LikeGo(bytes: number[]): string {
  const strict = new TextDecoder("utf-8", { fatal: true });
  let out = "";
  let i = 0;
  while (i < bytes.length) {
    const len = utf8SequenceLength(bytes[i]!);
    if (len === 1) {
      out += String.fromCharCode(bytes[i]!);
      i += 1;
      continue;
    }
    let decoded: string | null = null;
    if (len > 1 && i + len <= bytes.length) {
      try {
        decoded = strict.decode(Uint8Array.from(bytes.slice(i, i + len)));
      } catch {
        decoded = null; // 継続 byte が不正 / surrogate 範囲 / overlong
      }
    }
    if (decoded === null) {
      out += "�";
      i += 1;
    } else {
      out += decoded;
      i += len;
    }
  }
  return out;
}

/* patch を持つ file の path → パース済み patch の index。file type change は
 * 同 path で 2 entry になるため配列で持つ(先頭へ飛ばす)。key は生のパスへ
 * 正規化する — サイドバーは files[].path で引くため。 */
export function indexDiffFilesByPath(files: FileDiffMetadata[]): Map<string, number[]> {
  const byPath = new Map<string, number[]>();
  files.forEach((f, i) => {
    const path = unquoteGitPath(f.name);
    const at = byPath.get(path);
    if (at) at.push(i);
    else byPath.set(path, [i]);
  });
  return byPath;
}

/* contract の指示: truncated、またはいずれかの patchIncluded=false なら
 * 「review 対象が patch に揃っていない」ことを警告する。 */
export function diffWarning(d: DiffResponse): string | null {
  let omitted = 0;
  for (const f of d.files) if (!f.patchIncluded) omitted++;
  if (!d.truncated && omitted === 0) return null;
  const detail = omitted > 0 ? `${omitted} file の patch が省略されています` : "patch は不完全です";
  return `レビュー対象が patch に揃っていません — ${detail}`;
}

/* additions/deletions の合計。collectionLimit 行(null)は数えない。 */
export function diffTotals(files: DiffFileEntry[]): { additions: number; deletions: number } {
  let additions = 0;
  let deletions = 0;
  for (const f of files) {
    additions += f.additions ?? 0;
    deletions += f.deletions ?? 0;
  }
  return { additions, deletions };
}

export function diffMeta(d: DiffResponse): string {
  const totals = diffTotals(d.files);
  return (
    `merge-base ${d.mergeBase.slice(0, 10)} · captured ${clock(d.capturedAt)}` +
    ` · ${d.files.length} files +${totals.additions}/-${totals.deletions}`
  );
}

/* /api/diff のエラー body は {"error":"message"}(text は敵性入力 — 呼び出し側は
 * テキストノードのみで描画)。token/405 の middleware エラーは text/plain。 */
export async function diffErrorMessage(res: Response): Promise<string> {
  let detail = "";
  try {
    const body: unknown = await res.json();
    if (body && typeof body === "object" && "error" in body) {
      detail = String((body as { error: unknown }).error);
    }
  } catch {
    /* JSON でない body(middleware の text/plain 等)は詳細なし */
  }
  const head =
    res.status === 404
      ? "diff を取得できません — worktree の記録が見つかりません(cleanup 済みか、サーバーが /api/diff 未対応の可能性)"
      : res.status === 502
        ? "サーバーが diff を安全に生成できませんでした"
        : `diff の取得に失敗しました (HTTP ${res.status})`;
  return detail ? `${head}: ${detail}` : head;
}
