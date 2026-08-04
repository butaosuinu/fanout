/* GitHub リンク生成。repo("owner/name")を検証し、形式不一致(空・予期しない
 * 文字)なら "" を返す — 呼び出し側(GhLink)はリンク化せず素テキストに落とす。
 * href は JSX の自動エスケープでは守れないので、検証はここで完結させる。 */

export function ghBase(repo: string): string {
  return /^[\w.-]+\/[\w.-]+$/.test(repo) ? `https://github.com/${repo}` : "";
}

/* n > 0 ガード: @manual ペインの合成番号(-1, -2, …)は GitHub issue ではない
 * ので、リンク化せず素テキスト表示に落とす(repo 未解決時 fallback と同様)。 */
export function issueUrl(repo: string, n: number): string {
  const base = ghBase(repo);
  return base && n > 0 ? `${base}/issues/${n}` : "";
}

export function prUrl(repo: string, n: number): string {
  const base = ghBase(repo);
  return base && n > 0 ? `${base}/pull/${n}` : "";
}

/* Issue-mode parents are plain numbers; Project-mode parents are already a
 * github.com Projects URL(プレフィックス検証つきパススルー)。それ以外は "". */
export function parentUrl(repo: string, parent: string): string {
  if (/^\d+$/.test(parent)) return issueUrl(repo, Number(parent));
  if (parent.startsWith("https://github.com/")) return parent;
  return "";
}

/* Issue-mode parents get a "#" prefix; Projects URL は短縮表示。 */
export function parentLabel(parent: string): string {
  return /^\d+$/.test(parent) ? `#${parent}` : parent.replace(/^https:\/\/github\.com\//, "");
}
