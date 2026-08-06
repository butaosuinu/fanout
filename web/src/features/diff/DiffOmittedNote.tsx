import { Plural, useLingui } from "@lingui/react/macro";
import { OMITTED_REASON_LABELS } from "./diff";
import type { DiffFileEntry } from "../../transport/types";

/* path も omittedReason もサーバー(= worktree)由来の敵性入力。テキストノードと
 * して描くだけで、HTML としては解釈しない。 */

/* patch に含まれなかった file の一覧。
 *
 * サイドバーには置かない — サイドバーは本文が狭いと畳まれるので、そこにしか
 * 無いと「どの file がなぜレビューできないか」が狭い画面で丸ごと消える。
 * 警告帯の直下、本文領域の外に置いて常に見えるようにする。件数が上限
 * (collectionLimit)まで伸びうるので既定は畳んでおく。 */
export function DiffOmittedNote({ files }: { files: DiffFileEntry[] }) {
  const { i18n, t } = useLingui();
  const omitted = files.filter((f) => !f.patchIncluded);
  if (!omitted.length) return null;
  return (
    <details className="diff-omitted">
      <summary>
        <Plural value={omitted.length} other="# ファイルは patch に含まれていません" />
      </summary>
      <ul>
        {omitted.map((f) => (
          <li key={f.path}>
            <span className="diff-omitted-path">
              {f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}
            </span>
            <span className="diff-file-omitted">
              {f.omittedReason ? i18n._(OMITTED_REASON_LABELS[f.omittedReason]) : t`省略`}
            </span>
          </li>
        ))}
      </ul>
    </details>
  );
}
