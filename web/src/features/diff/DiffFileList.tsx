import { Trans, useLingui } from "@lingui/react/macro";
import { memo, type ReactElement } from "react";
import {
  CHANGE_KIND_LABELS,
  type DiffChangeKind,
  diffTotals,
  fileBase,
  fileDir,
  groupDiffFilesByDir,
} from "./diff";
import type { DiffFileEntry } from "../../transport/types";
import {
  IconButton,
  IconFileAdded,
  IconFileDeleted,
  IconFileModified,
  IconFileRenamed,
  IconFold,
  IconUnfold,
} from "../../ui/icons";

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

/* 移動元のうち「変わったほう」を出す。ディレクトリ移動なら旧ディレクトリ、
 * 同じディレクトリ内の改名なら旧ファイル名。両側に basename を出すと、
 * components/App.tsx → app/App.tsx が `App.tsx ← App.tsx` になって
 * 移動元を示せない。 */
function renameOrigin(file: DiffFileEntry): string | null {
  if (!file.oldPath) return null;
  const from = fileDir(file.oldPath);
  if (from === fileDir(file.path)) return fileBase(file.oldPath);
  return from || file.oldPath;
}

const KIND_ICONS: Record<DiffChangeKind, () => ReactElement> = {
  added: IconFileAdded,
  modified: IconFileModified,
  deleted: IconFileDeleted,
  renamed: IconFileRenamed,
};

/* 変更種別。アイコンは aria-hidden なので、意味は行の accessible name が持つ
 * (下の fileRowLabel)。種別が引けないときは何も描かない — 一覧に出るのは
 * patch を持つ file だけなので通常は起きないが、嘘のアイコンよりは無印がよい。 */
function KindIcon({ kind }: { kind: DiffChangeKind | undefined }) {
  if (!kind) return null;
  const Icon = KIND_ICONS[kind];
  return (
    <span className={`diff-file-kind k-${kind}`}>
      <Icon />
    </span>
  );
}

/* 移動した file は basename だけだと移動元が消える — group 見出しは移動先の
 * ディレクトリなので、行にも移動元を出さないと「どこから来たか」が失われる。 */
function FileRowBody({ file, kind }: { file: DiffFileEntry; kind: DiffChangeKind | undefined }) {
  const from = renameOrigin(file);
  return (
    <>
      <KindIcon kind={kind} />
      <span className="diff-file-name">{fileBase(file.path)}</span>
      {from ? <span className="diff-file-was">← {from}</span> : null}
      <Stat file={file} />
    </>
  );
}

/* 行の accessible name。移動は両端が揃って初めて意味を持つ。種別を前置するのは
 * アイコンが読み上げられないため — 行は button に明示的な aria-label を持つので、
 * 子要素の SVG にラベルを付けても accessible name には入らない。 */
function fileRowLabel(file: DiffFileEntry, kind: string): string {
  const path = file.oldPath ? `${file.oldPath} → ${file.path}` : file.path;
  return kind ? `${kind} ${path}` : path;
}

/* diff オーバーレイのサイドバー。サーバーの files[] をそのまま出すので、patch に
 * 含まれない file(バイナリ・サイズ超過・上限で省略)も欠落させずに並ぶ。
 * patch を持つ file はクリックで本文の該当 file へ飛ぶ。 */
export const DiffFileList = memo(function DiffFileList({
  files,
  selectable,
  kinds,
  onSelect,
  onExpandAll,
  onCollapseAll,
}: {
  files: DiffFileEntry[];
  /* patch にブロックがあり、本文へ飛べる path */
  selectable: ReadonlySet<string>;
  /* path → 変更種別。patch のパース結果由来(diff.ts の indexDiffKindsByPath) */
  kinds: ReadonlyMap<string, DiffChangeKind>;
  onSelect: (path: string) => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
}) {
  /* memo 境界の内側で locale を購読する。props はロケールに依存しないので、
   * これが無いと言語を切り替えても見出しとボタン名が古いまま残る。 */
  const { i18n, t } = useLingui();
  const totals = diffTotals(files);
  /* patch を持たない file は本文側の DiffOmittedNote が常時出す。ここは
   * 「飛べる file」だけにして、一覧の意味を移動先に絞る。 */
  const groups = groupDiffFilesByDir(files.filter((f) => f.patchIncluded));
  return (
    // nav ではなく region — 主目的は一覧で、移動は付随機能
    <section className="diff-sidebar" aria-label={t`変更ファイル`}>
      <div className="diff-sidebar-head">
        <span className="diff-sidebar-count">
          {files.length} files <span className="add">+{totals.additions}</span>
          <span className="del">-{totals.deletions}</span>
        </span>
        <span className="diff-sidebar-acts">
          <IconButton label={t`すべて展開`} onClick={onExpandAll}>
            <IconUnfold />
          </IconButton>
          <IconButton label={t`すべて折りたたむ`} onClick={onCollapseAll}>
            <IconFold />
          </IconButton>
        </span>
      </div>
      {groups.map((g) => (
        <div className="diff-file-group" key={g.dir}>
          <h4 className="diff-file-dir" title={g.dir}>
            {g.dir || <Trans>(リポジトリ直下)</Trans>}
          </h4>
          <ul className="diff-file-rows">
            {g.files.map((f) => {
              const kind = kinds.get(f.path);
              const label = fileRowLabel(f, kind ? i18n._(CHANGE_KIND_LABELS[kind]) : "");
              return (
                <li key={f.path}>
                  {selectable.has(f.path) ? (
                    /* 名前にフルパスを入れる — basename だけだと
                       src/index.ts と test/index.ts が同名になり、支援技術の
                       ボタン一覧から移動先を区別できない。title は子テキストの
                       ある要素では accessible name にならない。 */
                    <button
                      type="button"
                      className="diff-file-row"
                      title={label}
                      aria-label={label}
                      onClick={() => onSelect(f.path)}
                    >
                      <FileRowBody file={f} kind={kind} />
                    </button>
                  ) : (
                    <span className="diff-file-row is-static" title={label}>
                      <FileRowBody file={f} kind={kind} />
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
