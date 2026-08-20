import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import {
  useDiffHideViewed,
  useDiffLayout,
  useDiffView,
  type DiffLayout,
} from "../settings/useSettings";
import {
  IconButton,
  IconClose,
  IconEye,
  IconEyeOff,
  IconLayoutAuto,
  IconLayoutSplit,
  IconLayoutStack,
  IconMaximize,
  IconMinimize,
  IconRefresh,
  IconTheme,
} from "../../ui/icons";
import { MergeSplitButton } from "../merge/MergeSplitButton";
import { useMergeSlot } from "../merge/MergeSlot";

/* 並べ方ボタンは 1 個で auto -> split -> stack を巡回する */
const LAYOUT_CYCLE: Record<DiffLayout, DiffLayout> = {
  auto: "split",
  split: "stack",
  stack: "auto",
};
/* モジュール定数は import 時に一度だけ評価されるので、翻訳済み文字列ではなく
 * descriptor を置き、描画時に i18n._() で解決する。 */
const LAYOUT_LABELS: Record<DiffLayout, MessageDescriptor> = {
  auto: msg`自動`,
  split: msg`左右 2 面`,
  stack: msg`縦積み`,
};

function LayoutIcon({ layout, stack }: { layout: DiffLayout; stack: boolean }) {
  if (layout === "auto") return <IconLayoutAuto stack={stack} />;
  return layout === "split" ? <IconLayoutSplit /> : <IconLayoutStack />;
}

/* 何の差分を見ているか。対象名 + head → base + 統計行(取得前は統計だけ空)。 */
function DiffHeading({
  title,
  branches,
  meta,
}: {
  title: string;
  branches: { head: string; base: string } | null;
  meta: string;
}) {
  return (
    <>
      <h3>
        <span className="diff-title">{title}</span>
        {branches && (
          <span className="diff-branches">
            <code>{branches.head}</code> → <code>{branches.base}</code>
          </span>
        )}
      </h3>
      <span className="diff-meta" id="diff-meta">
        {meta}
      </span>
    </>
  );
}

/* オーバーレイのヘッダ。ラベルは aria-label とツールチップ(data-tip)が持ち、
 * ボタン本体はアイコンだけにして、狭いコンパクト表示でもヘッダを 1 行に収める。
 * 表示の設定は module store なので、値は props で降ろさずここで直接購読する。 */
export function DiffToolbar({
  title,
  branches,
  meta,
  loading,
  stack,
  onRefetch,
  onOpenSettings,
  onClose,
}: {
  title: string;
  branches: { head: string; base: string } | null;
  meta: string;
  loading: boolean;
  /* 解決後の並べ方。auto アイコンが向きを合わせるのに要る */
  stack: boolean;
  onRefetch: () => void;
  onOpenSettings: () => void;
  onClose: () => void;
}) {
  const { i18n, t } = useLingui();
  const { view, setView } = useDiffView();
  const { layout, setLayout } = useDiffLayout();
  const { hideViewed, setHideViewed } = useDiffHideViewed();
  /* diff の wire に PR は無いので、行の情報は MergeSlot 経由で受け取る */
  const merge = useMergeSlot();
  return (
    <header className="diff-head">
      <DiffHeading title={title} branches={branches} meta={meta} />
      {/* 不可逆な操作はラベル付きにして、可逆なアイコン群と語彙を分ける。
          右クラスタの先頭で、margin-left:auto の起点は merge.css が引き取る。 */}
      {merge && <MergeSplitButton id="diff-merge" merge={merge} />}
      <IconButton
        id="diff-reload"
        className="diff-reload"
        label={t`再取得`}
        disabled={loading}
        onClick={onRefetch}
      >
        <IconRefresh />
      </IconButton>
      {/* 確認済みの絞り込み。サイドバーは container query で消えるので、そこに
          置くと隠したまま戻せなくなる。ヘッダなら常に届く。 */}
      <IconButton
        id="diff-hide-viewed"
        label={hideViewed ? t`確認済みも表示` : t`確認済みを隠す`}
        onClick={() => setHideViewed(!hideViewed)}
      >
        {hideViewed ? <IconEye /> : <IconEyeOff />}
      </IconButton>
      {/* 1 個で auto -> split -> stack を巡回する。ラベルは現在の設定と、
          押したときに何になるかの両方を名乗る。 */}
      <IconButton
        id="diff-layout"
        label={t`レイアウト: ${{ current: i18n._(LAYOUT_LABELS[layout]) }}(クリックで${{
          next: i18n._(LAYOUT_LABELS[LAYOUT_CYCLE[layout]]),
        }})`}
        onClick={() => setLayout(LAYOUT_CYCLE[layout])}
      >
        <LayoutIcon layout={layout} stack={stack} />
      </IconButton>
      <IconButton
        id="diff-view-mode"
        label={view === "full" ? t`コンパクト表示` : t`全画面表示`}
        onClick={() => setView(view === "full" ? "compact" : "full")}
      >
        {view === "full" ? <IconMinimize /> : <IconMaximize />}
      </IconButton>
      <IconButton id="diff-settings" label={t`テーマ設定`} popup onClick={onOpenSettings}>
        <IconTheme />
      </IconButton>
      <IconButton id="diff-close" label={t`diff を閉じる`} onClick={onClose}>
        <IconClose />
      </IconButton>
    </header>
  );
}
