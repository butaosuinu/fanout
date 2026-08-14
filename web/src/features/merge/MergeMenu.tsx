import type { MessageDescriptor } from "@lingui/core";
import { useLingui } from "@lingui/react/macro";
import { useEffect, useRef, type KeyboardEvent } from "react";
import { IconCheck } from "../../ui/icons";
import { useMergeOptions, type MergeMethod } from "../settings/useSettings";
import { MERGE_METHOD_LABELS } from "./merge";

const METHODS: readonly MergeMethod[] = ["squash", "merge", "rebase"];

/* メニュー 1 項目。ラジオとチェックの違いは role と aria-checked だけなので、
 * マークアップは 1 箇所に置く。 */
function MergeItem({
  role,
  checked,
  label,
  itemRef,
  onClick,
}: {
  role: "menuitemradio" | "menuitemcheckbox";
  checked: boolean;
  label: string;
  itemRef: (el: HTMLButtonElement | null) => void;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      role={role}
      className="merge-opt"
      aria-checked={checked}
      ref={itemRef}
      onClick={onClick}
    >
      <span className="merge-mark" aria-hidden="true">
        {checked && <IconCheck />}
      </span>
      {label}
    </button>
  );
}

/* メニューの roving フォーカスとキー操作(APG のメニューパターン)。
 *
 * Escape は preventDefault + stopPropagation する。Drawer の document keydown と
 * diff オーバーレイの capture 段 listener の両方に漏らさないため。 */
function useMenuKeys(initialIndex: number, onClose: (refocus: boolean) => void) {
  const itemRefs = useRef<(HTMLButtonElement | null)[]>([]);

  /* 開いた直後は現在の方式にフォーカスする。以降の移動は矢印ハンドラが直接
   * focus() を呼ぶので、deps は mount 時のみで足りる。 */
  useEffect(() => {
    itemRefs.current[initialIndex]?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const move = (delta: 1 | -1) => {
    const count = itemRefs.current.length;
    const from = itemRefs.current.findIndex((el) => el === document.activeElement);
    itemRefs.current[(Math.max(from, 0) + delta + count) % count]?.focus();
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onClose(true);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      move(e.key === "ArrowDown" ? 1 : -1);
    } else if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      itemRefs.current[e.key === "Home" ? 0 : itemRefs.current.length - 1]?.focus();
    }
  };

  return { itemRefs, onKeyDown };
}

/* マージ方式のメニュー。方式を選ぶとその方式で即マージする(確認ダイアログは
 * 挟まない)。ブランチ削除はここには無い — マージ後に現れる別ボタンで、GitHub
 * 自身の "Delete branch" と同じ扱い。
 *
 * Escape は stopPropagation する。diff オーバーレイの useEscapeToClose は
 * document の capture 段に居るので、それだけでは足りず、あちら側もこのメニューが
 * 開いている間は譲る(useDiffOverlayModal を参照)。 */
export function MergeMenu({
  id,
  warnings,
  onMerge,
  onClose,
}: {
  id: string;
  warnings: MessageDescriptor[];
  onMerge: (method: MergeMethod) => void;
  onClose: (refocus: boolean) => void;
}) {
  const { i18n } = useLingui();
  const { method, setMethod } = useMergeOptions();
  const { itemRefs, onKeyDown } = useMenuKeys(METHODS.indexOf(method), onClose);

  const pick = (picked: MergeMethod) => {
    setMethod(picked);
    onClose(false);
    onMerge(picked);
  };

  return (
    <div className="merge-menu" role="menu" id={id} tabIndex={-1} onKeyDown={onKeyDown}>
      {warnings.length > 0 && (
        <ul className="merge-warn" role="status">
          {warnings.map((w) => (
            <li key={w.id ?? String(w.message)}>{i18n._(w)}</li>
          ))}
        </ul>
      )}
      {METHODS.map((m, i) => (
        <MergeItem
          key={m}
          role="menuitemradio"
          checked={m === method}
          label={i18n._(MERGE_METHOD_LABELS[m])}
          itemRef={(el) => {
            itemRefs.current[i] = el;
          }}
          onClick={() => pick(m)}
        />
      ))}
    </div>
  );
}
