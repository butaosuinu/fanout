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

/* サーバーの byte 上限(1 MiB 応答)は DOM コストを制限しない。worktree 由来の
 * patch は敵性入力で、契約内でも次を作れる:
 *   (a) 改行だけの 256 KiB ファイル → 約 26 万行
 *   (b) 交互トークン(`a+1+`…)の高密度行 → Shiki が 1 文字 1 span まで出す
 *   (c) 399 文字の置換行 → 行内 word 差分が decoration span を大量に生む
 * 平均実測値で見積もると破られるため、コストは最悪ケースで数える:
 *   highlight       1 文字あたり span + text の 2 node
 *   inline diff     さらに 1 文字あたり 1 node(実測 0.5 decoration/文字 × 2)
 *   行の枠          1 行あたり gutter/wrapper で 8 node
 * 見積りは実測と誤差 5% 以内(300 行 × 60 文字 = 予測 38,400 / 実測 37,312、
 * 1,500 行 × 40 文字 = 予測 132,000 / 実測 126,112)。
 *
 * 予算に収まらない file は 2 段階で軽くする:
 *   1. inline diff だけ切って highlight は残す(見積り 2 node/文字へ)
 *   2. それでも収まらなければ collapsed(ヘッダのみ)にして展開をクリックに委ねる
 * 展開時は highlight も inline diff も切るので、クリック後も固まらない —
 * 上記 (b) の 2 file 版は既定で 262,112 node / 11.9 秒、この経路で
 * 1,016 node / 113ms、(c) の 500 行 × 399 文字は 6,065ms → 349ms。 */
export const NODES_PER_CHAR = 2;
export const NODES_PER_CHAR_INLINE_DIFF = 1;
export const NODES_PER_LINE = 8;
/* FileDiff を 1 つ mount するだけで乗る固定コスト(shadow DOM の header、
 * SVG sprite、style)。実測 108 node/instance で、collapsed でも同じだけ
 * 掛かる — 契約上限の 500 files を collapsed で並べるだけで 54,000 node に
 * なる。そのため collapsed file は FileDiff を mount せず自前の軽量行で出し、
 * mount する file にはこの固定分を予算から引く。 */
export const FIXED_NODES_PER_FILE = 120;
export const MAX_FILE_RENDER_NODES = 40_000;
export const MAX_TOTAL_RENDER_NODES = 60_000;

/* 展開(plaintext 描画)しても許容できる行数の上限。highlight と inline diff を
 * 切っても 1 行あたり gutter/wrapper が残るため、行数だけで固まらせられる —
 * 契約内で作れる 256 KiB の改行のみ file は約 26 万行、実測では 20,000 行の
 * plaintext で 100,112 node / 1.1 秒だった。これを超える file は展開させず、
 * 行数だけ表示する(レビューは TUI か GitHub PR 側に委ねる)。 */
export const MAX_EXPANDABLE_LINES = 8_000;

export const TOKENIZE_MAX_LINE_LENGTH = 400;

/* isDiffMassive(行数 > tokenizeMaxLength)を必ず満たさせて plaintext 描画に
 * 落とすための値。展開された予算超過 file に使う。 */
export const TOKENIZE_MAX_LENGTH_PLAIN = 0;

/* 予算超過 file を展開するときは highlight だけでなく inline diff も切る。
 * ライブラリ既定の `word-alt` は plaintext 描画でも残り、行内 word 差分の
 * 計算と decoration span を生む。ライブラリ自身が inline diff を自動停止する
 * のは 1,000 行超のときだけなので、ちょうど 1,000 行の置換 patch は素通りする
 * (実測: 500 行 × 399 文字の置換 2 side で 6,065ms → `none` で 287ms、
 * decoration 1,500 → 0)。 */
export const LINE_DIFF_TYPE_PLAIN = "none";

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

/* 初期 mount した場合の最悪ケース DOM node 数の見積り。inline diff(行内 word
 * 差分)を有効にすると decoration が上乗せされるので、その分を含めた見積りと
 * 含めない見積りを分けて持つ。 */
export function estimatedRenderNodes(f: FileDiffMetadata, withInlineDiff = false): number {
  const perChar = withInlineDiff ? NODES_PER_CHAR + NODES_PER_CHAR_INLINE_DIFF : NODES_PER_CHAR;
  return (
    FIXED_NODES_PER_FILE + perChar * renderedCharCount(f) + NODES_PER_LINE * renderedLineCount(f)
  );
}

export interface RenderPlanEntry {
  file: FileDiffMetadata;
  lines: number;
  nodes: number;
  /* 予算に収まらず collapsed で mount する(展開はクリック) */
  overBudget: boolean;
  /* 行内 word 差分を有効にできる(予算に余裕がある) */
  inlineDiff: boolean;
  /* 展開させると行数だけで固まるため、展開自体を許さない */
  tooLargeToExpand: boolean;
}

export function planFileRendering(files: FileDiffMetadata[]): RenderPlanEntry[] {
  let budget = MAX_TOTAL_RENDER_NODES;
  return files.map((file) => {
    const lines = renderedLineCount(file);
    const withInline = estimatedRenderNodes(file, true);
    const plain = estimatedRenderNodes(file);
    const fits = (n: number) => n <= MAX_FILE_RENDER_NODES && n <= budget;
    if (fits(withInline)) {
      budget -= withInline;
      return {
        file,
        lines,
        nodes: withInline,
        overBudget: false,
        inlineDiff: true,
        tooLargeToExpand: false,
      };
    }
    if (fits(plain)) {
      budget -= plain;
      return {
        file,
        lines,
        nodes: plain,
        overBudget: false,
        inlineDiff: false,
        tooLargeToExpand: false,
      };
    }
    return {
      file,
      lines,
      nodes: plain,
      overBudget: true,
      inlineDiff: false,
      tooLargeToExpand: lines > MAX_EXPANDABLE_LINES,
    };
  });
}

export const OMITTED_REASON_LABELS: Record<Exclude<DiffOmittedReason, "">, string> = {
  binary: "バイナリのため patch なし",
  tooLarge: "サイズ上限超過のため patch なし",
  collectionLimit: "収集上限(10 MiB)で省略",
  responseLimit: "応答上限(1 MiB)で省略",
};

export function omittedFiles(files: DiffFileEntry[]): DiffFileEntry[] {
  return files.filter((f) => !f.patchIncluded);
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
