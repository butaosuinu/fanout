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

/* サーバーの byte 上限(1 MiB 応答)は DOM 行数を制限しない — 契約内でも
 * 改行だけの 256 KiB ファイルは約 26 万行に展開され、非仮想の FileDiff を
 * メインスレッドで停止させ得る(worktree 由来の patch は敵性入力)。描画は
 * 行数予算内の file に限り、超過分は省略として一覧に落とす。 */
export const MAX_FILE_RENDER_LINES = 5_000;
export const MAX_TOTAL_RENDER_LINES = 20_000;

export interface SuppressedFile {
  name: string;
  lines: number;
}

export function partitionRenderableFiles(files: FileDiffMetadata[]): {
  rendered: FileDiffMetadata[];
  suppressed: SuppressedFile[];
} {
  const rendered: FileDiffMetadata[] = [];
  const suppressed: SuppressedFile[] = [];
  let budget = MAX_TOTAL_RENDER_LINES;
  for (const f of files) {
    const lines = f.unifiedLineCount;
    if (lines > MAX_FILE_RENDER_LINES || lines > budget) {
      suppressed.push({ name: f.name, lines });
      continue;
    }
    budget -= lines;
    rendered.push(f);
  }
  return { rendered, suppressed };
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
