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
 * 平均実測値で見積もると (b) に破られるため、コストは最悪ケースで数える:
 * 1 文字あたり span + text node の 2 node、1 行あたり gutter/wrapper で 8 node。
 * この見積りは実測と誤差 5% 以内(300 行 × 60 文字 = 予測 38,400 / 実測 37,312、
 * 1,500 行 × 40 文字 = 予測 132,000 / 実測 126,112)。
 *
 * 予算超過 file は collapsed(ヘッダのみ)で mount し、展開はユーザーの
 * クリックに委ねる。展開時は highlight を切って(TOKENIZE_MAX_LENGTH_PLAIN)
 * 描画するので、クリック後も固まらない — 上記 (b) の 2 file 版は highlight
 * ありで 262,112 node / 11.9 秒、highlight なしで 1,016 node / 113ms。 */
export const NODES_PER_CHAR = 2;
export const NODES_PER_LINE = 8;
export const MAX_FILE_RENDER_NODES = 40_000;
export const MAX_TOTAL_RENDER_NODES = 60_000;

/* Shiki に長い 1 行を token 分解させない閾値。これを超える行は 1 span に
 * 落ちるため、行長方向の span 爆発を根元で止める(上記 (b) の実測: 400 で
 * 262,112 node → 1,016 node)。 */
export const TOKENIZE_MAX_LINE_LENGTH = 400;

/* isDiffMassive(行数 > tokenizeMaxLength)を必ず満たさせて plaintext 描画に
 * 落とすための値。展開された予算超過 file に使う。 */
export const TOKENIZE_MAX_LENGTH_PLAIN = 0;

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

/* highlight ありで初期 mount した場合の最悪ケース DOM node 数の見積り。 */
export function estimatedRenderNodes(f: FileDiffMetadata): number {
  return NODES_PER_CHAR * renderedCharCount(f) + NODES_PER_LINE * renderedLineCount(f);
}

export interface RenderPlanEntry {
  file: FileDiffMetadata;
  lines: number;
  nodes: number;
  overBudget: boolean;
}

export function planFileRendering(files: FileDiffMetadata[]): RenderPlanEntry[] {
  let budget = MAX_TOTAL_RENDER_NODES;
  return files.map((file) => {
    const nodes = estimatedRenderNodes(file);
    const overBudget = nodes > MAX_FILE_RENDER_NODES || nodes > budget;
    if (!overBudget) budget -= nodes;
    return { file, lines: renderedLineCount(file), nodes, overBudget };
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
