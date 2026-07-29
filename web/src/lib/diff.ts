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

/* サーバーの byte 上限(1 MiB 応答)は DOM コストを制限しない — 契約内でも
 * (a) 改行だけの 256 KiB ファイルは約 26 万行に展開され、(b) token 高密度の
 * TypeScript なら約 1,000 行で 36 万 HAST 要素を生成し得る(worktree 由来の
 * patch は敵性入力)。初期 mount は行数と文字数の両予算内に抑え、超過 file は
 * collapsed(ヘッダのみ)で出して展開をユーザーのクリックに委ねる。実測で
 * 1 描画行 ≈ 8 要素、1 文字 ≈ 0.4 token 要素なので、予算いっぱいでも初期
 * mount は 5 万要素程度に収まる。 */
export const MAX_FILE_RENDER_LINES = 1_500;
export const MAX_TOTAL_RENDER_LINES = 6_000;
export const MAX_FILE_RENDER_CHARS = 65_536; // 64 KiB
export const MAX_TOTAL_RENDER_CHARS = 131_072; // 128 KiB

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

export interface RenderPlanEntry {
  file: FileDiffMetadata;
  lines: number;
  chars: number;
  overBudget: boolean;
}

export function planFileRendering(files: FileDiffMetadata[]): RenderPlanEntry[] {
  let lineBudget = MAX_TOTAL_RENDER_LINES;
  let charBudget = MAX_TOTAL_RENDER_CHARS;
  return files.map((file) => {
    const lines = renderedLineCount(file);
    const chars = renderedCharCount(file);
    const overBudget =
      lines > MAX_FILE_RENDER_LINES ||
      chars > MAX_FILE_RENDER_CHARS ||
      lines > lineBudget ||
      chars > charBudget;
    if (!overBudget) {
      lineBudget -= lines;
      charBudget -= chars;
    }
    return { file, lines, chars, overBudget };
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
