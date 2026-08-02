import { memo } from "react";
import { diffTotals, fileBase, groupDiffFilesByDir } from "../lib/diff";
import type { DiffFileEntry } from "../lib/types";
import { IconButton, IconFold, IconUnfold } from "./icons";

/* path も omittedReason もサーバー(= worktree)由来の敵性入力。テキストノードと
 * して描くだけで、HTML としては解釈しない。 */

function Stat({ file }: { file: DiffFileEntry }) {
  /* collectionLimit の行は統計を持たない(contract 上 null) */
  if (file.additions === null || file.deletions === null) return <span className="muted">—</span>;
  return (
    <span className="diff-file-stat">
      <span className="add">+{file.additions}</span>
      <span className="del">-{file.deletions}</span>
    </span>
  );
}

/* diff オーバーレイのサイドバー。サーバーの files[] をそのまま出すので、patch に
 * 含まれない file(バイナリ・サイズ超過・上限で省略)も欠落させずに並ぶ。
 * patch を持つ file はクリックで本文の該当 file へ飛ぶ。 */
export const DiffFileList = memo(function DiffFileList({
  files,
  selectable,
  onSelect,
  onExpandAll,
  onCollapseAll,
}: {
  files: DiffFileEntry[];
  /* patch にブロックがあり、本文へ飛べる path */
  selectable: ReadonlySet<string>;
  onSelect: (path: string) => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
}) {
  const totals = diffTotals(files);
  /* patch を持たない file は本文側の DiffOmittedNote が常時出す。ここは
   * 「飛べる file」だけにして、一覧の意味を移動先に絞る。 */
  const groups = groupDiffFilesByDir(files.filter((f) => f.patchIncluded));
  return (
    // nav ではなく region — 主目的は一覧で、移動は付随機能
    <section className="diff-sidebar" aria-label="変更ファイル">
      <div className="diff-sidebar-head">
        <span className="diff-sidebar-count">
          {files.length} files <span className="add">+{totals.additions}</span>
          <span className="del">-{totals.deletions}</span>
        </span>
        <span className="diff-sidebar-acts">
          <IconButton label="すべて展開" onClick={onExpandAll}>
            <IconUnfold />
          </IconButton>
          <IconButton label="すべて折りたたむ" onClick={onCollapseAll}>
            <IconFold />
          </IconButton>
        </span>
      </div>
      {groups.map((g) => (
        <div className="diff-file-group" key={g.dir}>
          <h4 className="diff-file-dir" title={g.dir}>
            {g.dir || "(リポジトリ直下)"}
          </h4>
          <ul className="diff-file-rows">
            {g.files.map((f) => {
              const stat = <Stat file={f} />;
              const base = fileBase(f.path);
              return (
                <li key={f.path}>
                  {selectable.has(f.path) ? (
                    <button
                      type="button"
                      className="diff-file-row"
                      title={f.path}
                      onClick={() => onSelect(f.path)}
                    >
                      <span className="diff-file-name">{base}</span>
                      {stat}
                    </button>
                  ) : (
                    <span className="diff-file-row is-static" title={f.path}>
                      <span className="diff-file-name">{base}</span>
                      {stat}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </section>
  );
});
