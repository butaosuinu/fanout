import { Trans } from "@lingui/react/macro";
import { Virtualizer } from "@pierre/diffs/react";
import { memo, useCallback, useEffect, useMemo, type RefObject } from "react";
import { useStableCallback } from "../../shared/useStableCallback";
import type { DiffResponse } from "../../transport/types";
import { useDiffHideViewed, type Theme } from "../settings/useSettings";
import type { DiffFilePlan } from "./diff";
import { DiffFileList } from "./DiffFileList";
import { DiffFileRow } from "./DiffFileRow";
import { nextFileToRead } from "./scrollAlign";
import { useDiffCollapse } from "./useDiffCollapse";
import { useDiffPatch } from "./useDiffPatch";
import { useDiffScrolling } from "./useDiffScroller";
import { useDiffViewed } from "./useDiffViewed";
import { indicesForPaths } from "./viewed";

/* patch 本文と file path は敵性入力。@pierre/diffs はテキストをトークン分解して
 * DOM API で組み立てる(patch を HTML として解釈しない)前提で採用しており、
 * その性質は diffOverlay.test.tsx の敵性 patch テストで固定している。
 * こちら側でも patch 由来の文字列を dangerouslySetInnerHTML に渡さない。 */

const NO_INDICES: ReadonlySet<number> = new Set();

/* 画面外に先読みしておく高さ。既定の 1,000px は 1 フレームで消費されてしまい、
 * 高速スクロール中に描画が追いつかない。 */
const VIRTUALIZER_CONFIG = { overscrollSize: 3000 };

/* 焦点の行き先。まず「次に読む file」が差し出す操作 — 確認済みのチェック、それを
 * 持たない file(identity が曖昧で viewed.ts が fingerprint を出せない path)は
 * どの file にもある折りたたみボタン。
 * 行き先が無い(最後の file だった)ときは、焦点が実際に body へ落ちている場合だけ
 * 拾い直す: 残っているうちの先頭のチェックへ、それも無ければオーバーレイ自身へ。
 * 落ちていなければ null = 動かさない(無関係な file へ焦点を飛ばさない)。 */
function focusTargetIn(root: HTMLElement, index: number | null): HTMLElement | null {
  if (index !== null) {
    const file = `.diff-file[data-index="${index}"]`;
    const at = root.querySelector<HTMLElement>(
      `${file} .diff-file-viewed input, ${file} .icon-btn`,
    );
    if (at) return at;
  }
  if (document.activeElement !== document.body) return null;
  return root.querySelector<HTMLElement>(".diff-file .diff-file-viewed input") ?? root;
}

/* 確認済みを付けたあとの焦点を、本文が上端へ送る file と同じ file へ移す。
 *
 * 隠す設定では、チェックした行ごと unmount されて焦点が body へ落ち、トラップの外に
 * 出る。隠さない設定でも動かす必要がある — 送り先とずれたまま直前の file のチェックに
 * 焦点が残ると、画面に出ているのは次の file なのに Space がひとつ前の確認済みを外す。
 *
 * 判定も探索も次フレーム。DOM が入れ替わるのは commit 後だし、行き先付きの拾い直しを
 * 予約した直後は焦点が「落ちている」ように見えるので、body 判定もここまで遅らせる。
 * checkbox は shadow root へ slot されるが、実体は .diff-file の light DOM の子な
 * ので querySelector で届く。 */
function useFocusAfterViewed(
  rootRef: RefObject<HTMLElement | null>,
): (index: number | null) => void {
  return useCallback(
    (index) => {
      requestAnimationFrame(() => {
        const root = rootRef.current;
        if (!root) return;
        focusTargetIn(root, index)?.focus({ preventScroll: true });
      });
    },
    [rootRef],
  );
}

/* memo: Drawer は SSE snapshot tick(約 2s)ごとに再レンダーされるが、diff と
 * theme は変わらないので file 列全体をスキップさせる(library の FileDiff は
 * 非 memo で、素通しすると tick ごとに全 file の setOptions/render が走る)。
 * 折りたたみと確認済みの操作は Map / Set の差し替えで伝わり、実際に描き直すのは
 * 触った file だけ(DiffFileRow が memo なので)。 */
const DiffFiles = memo(function DiffFiles({
  plan,
  isCollapsed,
  viewed,
  viewable,
  hidden,
  theme,
  diffThemes,
  stack,
  registerHost,
  onToggle,
  onToggleViewed,
}: {
  plan: DiffFilePlan[];
  /* 畳まれているかの判定は useDiffCollapse が持つ(同じ式を 2 箇所に書かない) */
  isCollapsed: (index: number) => boolean;
  viewed: ReadonlySet<number>;
  /* チェックを出せる index。identity が曖昧な path は持てない(viewed.ts) */
  viewable: ReadonlySet<number>;
  /* 「確認済みを隠す」で本文から降ろす index。plan の index は詰めない —
   * 折りたたみ・host 登録・飛び先の索引がすべて index を key にしているため。 */
  hidden: ReadonlySet<number>;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  stack: boolean;
  registerHost: (index: number, el: HTMLDivElement | null) => void;
  onToggle: (index: number) => void;
  onToggleViewed: (index: number) => void;
}) {
  /* patch を持たない file(バイナリ・上限で省略)は plan に入らないので、
   * 「すべて」とは言わない — 警告帯と DiffOmittedNote がまだ残件を出している。 */
  if (plan.length > 0 && hidden.size === plan.length) {
    return (
      <div className="diff-note">
        <Trans>patch のあるファイルはすべて確認済みです</Trans>
      </div>
    );
  }
  return (
    <>
      {plan.map((entry, i) =>
        hidden.has(i) ? null : (
          <DiffFileRow
            // file type change は同 path が 2 entry になるため path 単独を key にしない
            key={`${i}:${entry.file.name}`}
            index={i}
            entry={entry}
            theme={theme}
            diffThemes={diffThemes}
            stack={stack}
            collapsed={isCollapsed(i)}
            viewed={viewed.has(i)}
            viewable={viewable.has(i)}
            registerHost={registerHost}
            onToggle={onToggle}
            onToggleViewed={onToggleViewed}
          />
        ),
      )}
    </>
  );
});

/* patch から出るものを 1 つにまとめた状態。オーバーレイの外枠(モーダル・幅・
 * 覆い判定)とは関心が別なので、hook もろとも本文側へ寄せてある。 */
function useDiffBodyState({
  patch,
  scopeKey,
  rootRef,
  layoutKey,
}: {
  patch: string;
  scopeKey: string;
  rootRef: RefObject<HTMLElement | null>;
  /* 全 file の高さが変わる操作の合成キー(表示モード・並べ方) */
  layoutKey: string;
}) {
  const { plan, byPath, selectable, kinds, paths, fingerprints } = useDiffPatch(patch);
  const { viewedPaths, setViewed } = useDiffViewed(scopeKey, fingerprints);
  const { hideViewed } = useDiffHideViewed();
  const viewed = useMemo(() => indicesForPaths(paths, viewedPaths), [paths, viewedPaths]);
  /* チェックを出せる file。identity が曖昧な path は fingerprint を持たない
   * (viewed.ts の sameFileGroup)ので、チェックしても何も起きない = 出さない。 */
  const viewable = useMemo(() => indicesForPaths(paths, fingerprints), [paths, fingerprints]);
  /* 隠すのは描画から降ろすだけで、plan の index は詰めない(DiffFiles を参照)。 */
  const hidden = hideViewed ? viewed : NO_INDICES;
  /* 折りたたみの上書きが効く範囲。patch だけで区切ると、同じ worktree を指す行
   * (attached-agent など)は patch が一致するので、行を切り替えても前の行の
   * 上書きが残り、確認済みで復元した file が開いたままになる。 */
  const collapse = useDiffCollapse(`${scopeKey}\n${patch}`, plan, viewed);
  const focusAfterViewed = useFocusAfterViewed(rootRef);
  /* 隠す / 出すも全 file の高さを変えるので、並べ方の切替と同じく取り直させる。 */
  const scrolling = useDiffScrolling({
    rootRef,
    byPath,
    expand: collapse.expand,
    patch,
    layoutKey: `${layoutKey}:${hidden.size}`,
  });

  /* 確認済みの結果としての折りたたみは、上書きを**消す**ことで表す(付ける側も
   * 外す側も)。畳むかどうかは `collapsedAt` が確認済みから導くので、上書きを書く
   * 必要が無い。
   *
   * `true` を書いてはいけない。上書きは確認済みより優先するので、別タブで外された
   * ときにチェックだけ外れて本文が畳まれたまま残る。`false` も書けない — 1,000 行超で
   * 既定折りたたみだった file が、開いたことすら無い状態から全開になる。
   * file type change は同 path が 2 entry になるため、path の全 index に及ぼす。 */
  const onToggleViewed = useStableCallback((i: number) => {
    const path = paths[i];
    if (path === undefined) return;
    const next = !viewedPaths.has(path);
    const group = byPath.get(path) ?? [];
    setViewed(path, next);
    for (const j of group) collapse.setCollapsed(j, null);
    /* 外す側は何も送らない — その file の上端は元から動かない。 */
    if (!next) return;
    /* 畳んだ(または消えた)ぶん文書が縮む。次に読む file を 1 つだけ決めて、
     * 焦点も本文の上端もそこへ揃える(別々に選ぶと食い違う)。 */
    const to = nextFileToRead({ from: i, count: plan.length, group, hidden });
    focusAfterViewed(to);
    if (to !== null) scrolling.alignAfterCommit(to);
  });

  return {
    plan,
    selectable,
    kinds,
    viewedPaths,
    fingerprints,
    viewed,
    viewable,
    hidden,
    hideViewed,
    collapse,
    scrolling,
    onToggleViewed,
    focusAfterViewed,
  };
}

/* オーバーレイの本文領域。左にファイル一覧、右に仮想化した file 列。 */
export function DiffBody({
  diff,
  scopeKey,
  theme,
  diffThemes,
  stack,
  covering,
  rootRef,
  layoutKey,
}: {
  diff: DiffResponse;
  scopeKey: string;
  theme: Theme;
  diffThemes: { light: string; dark: string };
  stack: boolean;
  /* 背面を覆っているか。焦点の拾い直しをここでだけ効かせる */
  covering: boolean;
  rootRef: RefObject<HTMLElement | null>;
  layoutKey: string;
}) {
  const s = useDiffBodyState({ patch: diff.patch, scopeKey, rootRef, layoutKey });
  const { hidden, focusAfterViewed } = s;
  /* 行が消える理由はローカル操作だけではない — 別タブが同じ scope でチェックすると
   * storage 経由でここでも消える。呼び出し点が無いので、隠れる集合が動いたあとに
   * 焦点が落ちていたら拾い直す。覆っているあいだだけ効かせる(コンパクトで背面を
   * 触っている人からフォーカスを奪わない)。
   *
   * 行き先を指定せずに呼ぶと「焦点が落ちていたら拾い直す」だけになる。落ちている
   * かどうかの判定は focusAfterViewed が次フレームまで遅らせる — commit 直後は
   * ローカル操作が予約した行き先付きの移動がまだ走っておらず、ここで判定すると
   * 「落ちている」と読めてしまい、あとから走るこちらが行き先付きを上書きする。 */
  useEffect(() => {
    if (covering) focusAfterViewed(null);
  }, [covering, hidden, focusAfterViewed]);

  if (diff.files.length === 0) {
    return (
      <div className="diff-note">
        <Trans>merge-base からの変更はありません</Trans>
      </div>
    );
  }
  return (
    <div className="diff-main">
      <DiffFileList
        files={diff.files}
        selectable={s.selectable}
        kinds={s.kinds}
        viewedPaths={s.viewedPaths}
        viewableCount={s.fingerprints.size}
        hideViewed={s.hideViewed}
        onSelect={s.scrolling.onSelectFile}
        onExpandAll={s.collapse.onExpandAll}
        onCollapseAll={s.collapse.onCollapseAll}
      />
      {/* Virtualizer の root がスクロールコンテナ、content が中身。画面外の
          file は高さだけ確保した placeholder になる。 */}
      <Virtualizer className="diff-body" contentClassName="diff-files" config={VIRTUALIZER_CONFIG}>
        <DiffFiles
          plan={s.plan}
          isCollapsed={s.collapse.isCollapsed}
          viewed={s.viewed}
          viewable={s.viewable}
          hidden={s.hidden}
          theme={theme}
          diffThemes={diffThemes}
          stack={stack}
          registerHost={s.scrolling.registerHost}
          onToggle={s.collapse.onToggle}
          onToggleViewed={s.onToggleViewed}
        />
      </Virtualizer>
    </div>
  );
}
