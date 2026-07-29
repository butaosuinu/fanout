import type { DiffFileEntry, DiffOmittedReason, DiffResponse, PaneView } from "./types";

/* GET /api/diff の行 identity クエリ(正は docs/local-diff-review-tools.ja.md)。
 * GitHub issue 行(issueNum>0)は parent+issue、plan task 行は parent+task+source、
 * 負の synthetic issue 行(@manual / attached-agent)は parent+issue+source。
 * identity を組めない行(shell、未開始、worktree 記録なし、source 必須なのに
 * sourceKey 欠落)は null を返し、呼び出し側はボタンを出さない。 */
export function diffQuery(parent: string, p: PaneView): Record<string, string> | null {
  if (p.notStarted || p.kind === "shell" || !p.worktreePath) return null;
  if (p.taskId) {
    return p.sourceKey ? { parent, task: p.taskId, source: p.sourceKey } : null;
  }
  if (p.issueNum > 0) return { parent, issue: String(p.issueNum) };
  if (p.issueNum < 0 && p.sourceKey) {
    return { parent, issue: String(p.issueNum), source: p.sourceKey };
  }
  return null;
}

/* 連結 patch を file block 単位へ分割する。@pierre/diffs の PatchDiff は
 * 「1 patch = 1 file diff」入力しか受けないため、描画前にここで割る。
 * 分割規則はライブラリ内部の GIT_DIFF_FILE_BREAK_REGEX と同じ行頭一致。
 * patch の内容行は必ず ' '/'+'/'-' で始まるので、行頭 "diff --git " は
 * block 境界にしか現れない。 */
export function splitPatch(patch: string): string[] {
  if (!patch) return [];
  return patch.split(/(?=^diff --git )/m).filter((b) => b.startsWith("diff --git "));
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
  const omitted = omittedFiles(d.files).length;
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
