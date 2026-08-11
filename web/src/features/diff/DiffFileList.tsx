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
  IconCheck,
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
 * (下の fileRowLabel)。種別が引けないときも span は残す — 行の左インデントは
 * この列が担っているので、要素ごと消すとその行だけ左へずれる。嘘のアイコンを
 * 描くよりは空欄がよい、が列そのものは畳まない。 */
function KindIcon({ kind }: { kind: DiffChangeKind | undefined }) {
  const Icon = kind ? KIND_ICONS[kind] : null;
  return (
    <span className={kind ? `diff-file-kind k-${kind}` : "diff-file-kind"}>
      {Icon ? <Icon /> : null}
    </span>
  );
}

/* 移動した file は basename だけだと移動元が消える — group 見出しは移動先の
 * ディレクトリなので、行にも移動元を出さないと「どこから来たか」が失われる。
 * 確認済みの印は行末に置く。ここは表示専用で、チェックの操作は本文の
 * ファイル名ヘッダが持つ(意味は行の accessible name が名乗る)。 */
function FileRowBody({
  file,
  kind,
  viewed,
}: {
  file: DiffFileEntry;
  kind: DiffChangeKind | undefined;
  viewed: boolean;
}) {
  const from = renameOrigin(file);
  return (
    <>
      <KindIcon kind={kind} />
      <span className="diff-file-name">{fileBase(file.path)}</span>
      {from ? <span className="diff-file-was">← {from}</span> : null}
      <Stat file={file} />
      {viewed ? (
        <span className="diff-file-check">
          <IconCheck />
        </span>
      ) : null}
    </>
  );
}

/* 行の accessible name。移動は両端が揃って初めて意味を持つ。種別も名乗るのは
 * アイコンが読み上げられないため — 行は button に明示的な aria-label を持つので、
 * 子要素の SVG にラベルを付けても accessible name には入らない。
 *
 * 種別は後置する。支援技術の要素一覧は accessible name の先頭から type-ahead で
 * 絞り込むので、前置すると 40 行が「変更 …」で始まり、パスを打っても目的の file
 * へ飛べなくなる。区切りは本文側の折りたたみボタン(`<path> — 折りたたむ`)に揃える。 */
function fileRowLabel(file: DiffFileEntry, kind: string, viewed: string): string {
  const path = file.oldPath ? `${file.oldPath} → ${file.path}` : file.path;
  return [path, kind, viewed].filter(Boolean).join(" — ");
}

/* 一覧の 1 行。飛べる file はボタン、飛べない file は静的なテキスト。
 * 名前にフルパスを入れる — basename だけだと src/index.ts と test/index.ts が
 * 同名になり、支援技術のボタン一覧から移動先を区別できない。title は子テキストの
 * ある要素では accessible name にならない。 */
function FileRow({
  file,
  kind,
  viewed,
  selectable,
  onSelect,
}: {
  file: DiffFileEntry;
  kind: DiffChangeKind | undefined;
  viewed: boolean;
  selectable: boolean;
  onSelect: (path: string) => void;
}) {
  const { i18n, t } = useLingui();
  const label = fileRowLabel(
    file,
    kind ? i18n._(CHANGE_KIND_LABELS[kind]) : "",
    viewed ? t`確認済み` : "",
  );
  const body = <FileRowBody file={file} kind={kind} viewed={viewed} />;
  const className = viewed ? "diff-file-row is-viewed" : "diff-file-row";
  if (!selectable) {
    return (
      <span className={`${className} is-static`} title={label}>
        {body}
      </span>
    );
  }
  return (
    <button
      type="button"
      className={className}
      title={label}
      aria-label={label}
      onClick={() => onSelect(file.path)}
    >
      {body}
    </button>
  );
}

/* 件数と一括操作。確認済みの進捗の分母は「本文にブロックがある file」— binary や
 * 省略された file はチェックを持てないので、母集団に入れると必ず届かない。 */
function SidebarHead({
  files,
  viewedCount,
  viewableCount,
  onExpandAll,
  onCollapseAll,
}: {
  files: DiffFileEntry[];
  viewedCount: number;
  viewableCount: number;
  onExpandAll: () => void;
  onCollapseAll: () => void;
}) {
  const { t } = useLingui();
  const totals = diffTotals(files);
  return (
    <div className="diff-sidebar-head">
      <span className="diff-sidebar-count">
        {files.length} files <span className="add">+{totals.additions}</span>
        <span className="del">-{totals.deletions}</span>
      </span>
      {viewableCount > 0 && (
        <span className="diff-sidebar-viewed">
          {t`${{ done: viewedCount }} / ${{ total: viewableCount }} 確認済み`}
        </span>
      )}
      <span className="diff-sidebar-acts">
        <IconButton label={t`すべて展開`} onClick={onExpandAll}>
          <IconUnfold />
        </IconButton>
        <IconButton label={t`すべて折りたたむ`} onClick={onCollapseAll}>
          <IconFold />
        </IconButton>
      </span>
    </div>
  );
}

/* diff オーバーレイのサイドバー。サーバーの files[] をそのまま出すので、patch に
 * 含まれない file(バイナリ・サイズ超過・上限で省略)も欠落させずに並ぶ。
 * patch を持つ file はクリックで本文の該当 file へ飛ぶ。 */
export const DiffFileList = memo(function DiffFileList({
  files,
  selectable,
  kinds,
  viewedPaths,
  hideViewed,
  onSelect,
  onExpandAll,
  onCollapseAll,
}: {
  files: DiffFileEntry[];
  /* patch にブロックがあり、本文へ飛べる path */
  selectable: ReadonlySet<string>;
  /* path → 変更種別。patch のパース結果由来(diff.ts の indexDiffKindsByPath) */
  kinds: ReadonlyMap<string, DiffChangeKind>;
  viewedPaths: ReadonlySet<string>;
  /* 本文と揃えて確認済みの行を降ろす。残すと飛び先の無いリンクになる */
  hideViewed: boolean;
  onSelect: (path: string) => void;
  onExpandAll: () => void;
  onCollapseAll: () => void;
}) {
  /* memo 境界の内側で locale を購読する。props はロケールに依存しないので、
   * これが無いと言語を切り替えても見出しとボタン名が古いまま残る。 */
  const { t } = useLingui();
  /* patch を持たない file は本文側の DiffOmittedNote が常時出す。ここは
   * 「飛べる file」だけにして、一覧の意味を移動先に絞る。 */
  const listed = files.filter((f) => f.patchIncluded && !(hideViewed && viewedPaths.has(f.path)));
  return (
    // nav ではなく region — 主目的は一覧で、移動は付随機能
    <section className="diff-sidebar" aria-label={t`変更ファイル`}>
      <SidebarHead
        files={files}
        viewedCount={viewedPaths.size}
        viewableCount={selectable.size}
        onExpandAll={onExpandAll}
        onCollapseAll={onCollapseAll}
      />
      {groupDiffFilesByDir(listed).map((g) => (
        <div className="diff-file-group" key={g.dir}>
          <h4 className="diff-file-dir" title={g.dir}>
            {g.dir || <Trans>(リポジトリ直下)</Trans>}
          </h4>
          <ul className="diff-file-rows">
            {g.files.map((f) => (
              <li key={f.path}>
                <FileRow
                  file={f}
                  kind={kinds.get(f.path)}
                  viewed={viewedPaths.has(f.path)}
                  selectable={selectable.has(f.path)}
                  onSelect={onSelect}
                />
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  );
});
