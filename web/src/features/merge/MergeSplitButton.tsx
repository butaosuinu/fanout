import { useLingui } from "@lingui/react/macro";
import { useEffect, useRef } from "react";
import { usePopover } from "../../shared/usePopover";
import { IconChevronDown } from "../../ui/icons";
import { useMergeOptions, type MergeMethod } from "../settings/useSettings";
import { MERGE_METHOD_LABELS } from "./merge";
import { MergeMenu } from "./MergeMenu";
import type { MergeAffordance } from "./useMergeFlow";

/* 送信結果。押した本人にだけ、押したボタンの隣に出る。確認ダイアログを持たない
 * 導線なので、結果を返す場所がここしかない。本文はサーバ由来の敵性入力なので
 * テキストノードのみで描く。 */
function MergeReport({ merge }: { merge: MergeAffordance }) {
  const { i18n } = useLingui();
  return (
    <>
      {merge.error && (
        <p className="merge-error" role="alert">
          {i18n._(merge.error)}
        </p>
      )}
      {merge.notice && (
        <p className="merge-note" role="status">
          {i18n._(merge.notice)}
        </p>
      )}
    </>
  );
}

/* ボタンの読み上げ名。無効な行では理由を名前に畳み込む — aria-disabled は
 * フォーカスを受けるので、キーボードからも理由に届く。 */
function useMergeLabels(
  merge: MergeAffordance,
  method: MergeMethod,
): { blocked: string; label: string; methodLabel: string } {
  const { i18n, t } = useLingui();
  const blocked = merge.blocked ? i18n._(merge.blocked) : "";
  return {
    blocked,
    label: blocked
      ? t`#${{ pr: merge.prNumber }} をマージ — ${{ reason: blocked }}`
      : t`#${{ pr: merge.prNumber }} をマージ`,
    methodLabel: t`マージ方式: ${{ method: i18n._(MERGE_METHOD_LABELS[method]) }}(クリックで変更)`,
  };
}

/* メニューは snapshot をまたいで開いたままになるが、その間に対象 PR や head が
 * 差し替わると、開いた時点とは別の commit を送ることになる。サーバ側の照合は
 * どちらも新しい値どうしなので通ってしまう。対象が動いたら閉じる。 */
function useCloseOnTargetChange(
  merge: MergeAffordance,
  open: boolean,
  close: (refocus: boolean) => void,
) {
  useEffect(() => {
    if (open) close(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [merge.prNumber, merge.headSha, merge.baseRef]);
}

/* 分割ボタン。本体は現在の方式で即マージし、caret は方式メニューを開く。
 *
 * 警告のある PR(CI 失敗・レビュー未承認・競合不明)では、本体を押しても即実行
 * せずメニューを開く。確認ダイアログを持たない構成で、警告を見せられる場所が
 * メニューしかないため。警告の無い PR は 1 クリックのまま。
 *
 * 無効時は disabled ではなく aria-disabled にする。disabled はフォーカスを
 * 受けられず、`.tip:focus-visible::after` が出ないのでキーボードから理由に
 * たどり着けない。 */
export function MergeSplitButton({ id, merge }: { id: string; merge: MergeAffordance }) {
  const { t } = useLingui();
  const { method } = useMergeOptions();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const { open, setOpen, close, onBlur } = usePopover(rootRef, triggerRef);
  const { blocked, label, methodLabel } = useMergeLabels(merge, method);
  useCloseOnTargetChange(merge, open, close);

  const menuId = `${id}-menu`;
  const viaMenu = merge.warnings.length > 0;
  const act = {
    /* 無効な行はどちらのボタンからも撃たせない。本体だけ塞いでも caret から方式を
     * 選べば実行でき、merged / 反映待ちの行で二重にマージが飛ぶ。 */
    merge: (picked: MergeMethod) => {
      if (!blocked) merge.onMerge(picked);
    },
    press: () => {
      if (blocked) return;
      if (viaMenu) setOpen(true);
      else merge.onMerge(method);
    },
    caret: () => {
      if (blocked) return;
      if (open) close(false);
      else setOpen(true);
    },
  };

  return (
    <div className="merge-split" ref={rootRef} onBlur={onBlur}>
      <button
        type="button"
        id={id}
        className="merge-go tip"
        aria-disabled={blocked ? true : undefined}
        aria-haspopup={!blocked && viaMenu ? "menu" : undefined}
        aria-label={label}
        data-tip={label}
        onClick={act.press}
      >
        {merge.sending ? t`マージ中…` : t`マージ`}
      </button>
      <button
        type="button"
        id={menuId}
        ref={triggerRef}
        className="merge-caret tip"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? `${menuId}-pop` : undefined}
        aria-disabled={blocked ? true : undefined}
        aria-label={methodLabel}
        data-tip={blocked || methodLabel}
        onClick={act.caret}
      >
        <IconChevronDown />
      </button>
      {open && (
        <MergeMenu
          id={`${menuId}-pop`}
          warnings={merge.warnings}
          onMerge={act.merge}
          onClose={close}
        />
      )}
      <MergeReport merge={merge} />
    </div>
  );
}
