import { Trans, useLingui } from "@lingui/react/macro";
import { memo, useEffect, useRef, useState, type FocusEvent, type KeyboardEvent } from "react";

export type Option = readonly [value: string, label: string];

/* GitHub PR 風フィルタードロップダウン(8 キー共通)。trigger ボタン +
 * role="listbox" の popover で、選択は onPickToken による key:value トークン
 * 書込のみ — 「#filter のテキストが単一の真実」という既存モデルは変えない。
 * アクティブ値の再クリックは onClearKey(同キーのトークンを全て外す
 * トグルオフ — 手打ちの重複キーも取り残さない)。
 *
 * - 開いている間は選択肢を凍結する: 2 秒 tick の snapshot 更新で開いたメニュー
 *   がズレたり閉じたりしない(旧 DynamicSelect の focus-freeze の置き換え)。
 * - キー処理はルート div に置く: popover が開いている間は trigger に
 *   フォーカスが戻っていても(Shift+Tab)Esc / 矢印 / typeahead が効く。
 *   Esc は popover を閉じて trigger にフォーカスを戻し、preventDefault +
 *   stopPropagation で Drawer の document keydown へ漏らさない(Drawer 側も
 *   defaultPrevented を見る)。
 * - option は flat button の roving tabindex(ArrowUp/Down で巡回、Enter で
 *   選択、非 searchable は先頭文字 typeahead)。searchable(agent / wave)は
 *   検索 input から ↓ で option 列に入る。 */
export const FilterDropdown = memo(function FilterDropdown({
  dataKey,
  ariaLabel,
  options,
  active,
  searchable = false,
  onPickToken,
  onClearKey,
}: {
  dataKey: string;
  ariaLabel: string;
  options: readonly Option[];
  active: string | null; // 適用中トークンの値(小文字)。tokenForKey の結果
  searchable?: boolean;
  onPickToken: (key: string, value: string) => void;
  onClearKey: (key: string) => void;
}) {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0); // roving tabindex の現在位置(visible 基準)
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const frozen = useRef(options);

  // 開いている間しか描画されないので、常に凍結値を読む(開く瞬間に再同期)
  const base = frozen.current;
  const q = query.trim().toLowerCase();
  const visible =
    searchable && q
      ? base.filter(([v, l]) => v.toLowerCase().includes(q) || l.toLowerCase().includes(q))
      : base;

  /* 手打ちトークンは label 表記の別名でも来る(wave:w2 — matches() が
   * fmtWave 経由で受け付ける)ので、value と label の両方で照合する */
  const isActive = ([v, l]: Option) => active === v.toLowerCase() || active === l.toLowerCase();
  const listId = `fd-list-${dataKey}`;

  const openPopover = () => {
    frozen.current = options; // 開く瞬間の選択肢で凍結
    setQuery("");
    const idx = active ? options.findIndex(isActive) : -1;
    setCursor(idx >= 0 ? idx : 0);
    setOpen(true);
  };
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) triggerRef.current?.focus();
  };
  const pick = (opt: Option) => {
    if (isActive(opt)) onClearKey(dataKey);
    else onPickToken(dataKey, opt[0]);
    close(true);
  };

  /* 開いた直後の初期フォーカス: searchable は検索 input、それ以外はアクティブ
   * (無ければ先頭)option。cursor 変化での再フォーカスは矢印キーハンドラが
   * 直接行うため、deps は意図的に open のみ。 */
  useEffect(() => {
    if (!open) return;
    if (searchable) searchRef.current?.focus();
    else optionRefs.current[cursor]?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  /* popover の外の pointerdown で閉じる(フォーカスは奪わない)。別ドロップ
   * ダウンの trigger 押下も「外」なので、同時に 2 つ開くことはない。 */
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (e.target instanceof Node && !rootRef.current?.contains(e.target)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  // cursor は option の onFocus が単一の書き手(click / Tab / 矢印すべて経由)
  const focusOption = (idx: number) => optionRefs.current[idx]?.focus();
  const moveCursor = (delta: 1 | -1) => {
    if (!visible.length) return;
    // 検索 input からの矢印キーは端から option 列に入る(↓=先頭 / ↑=末尾)
    const from = document.activeElement === searchRef.current ? (delta === 1 ? -1 : 0) : cursor;
    focusOption((from + delta + visible.length) % visible.length);
  };

  /* 非 searchable はネイティブ select 同等の先頭文字 typeahead(現在位置の
   * 次から巡回検索)。 */
  const typeahead = (ch: string) => {
    const n = visible.length;
    for (let i = 1; i <= n; i++) {
      const idx = (cursor + i) % n;
      if (visible[idx]?.[1].toLowerCase().startsWith(ch)) {
        focusOption(idx);
        return true;
      }
    }
    return false;
  };

  /* ルート div のキー処理: trigger にフォーカスが戻っていても popover が
   * 開いていれば効く(.fd-pop 限定にすると Esc が Drawer に漏れる)。 */
  const onRootKeyDown = (e: KeyboardEvent) => {
    if (!open || e.nativeEvent.isComposing) return; // IME 変換中のキーは奪わない
    const inSearch = document.activeElement === searchRef.current;
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation(); // Drawer の document keydown に漏らさない
      close(true);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      moveCursor(e.key === "ArrowDown" ? 1 : -1);
    } else if ((e.key === "Home" || e.key === "End") && !inSearch) {
      e.preventDefault();
      if (visible.length) focusOption(e.key === "Home" ? 0 : visible.length - 1);
    } else if (!searchable && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      if (typeahead(e.key.toLowerCase())) e.preventDefault();
    }
  };

  /* 検索 input の Enter は roving cursor 位置(既定 = アクティブ or 先頭の
   * 絞り込み結果)を選択。IME の変換確定 Enter は奪わない。 */
  const onSearchKeyDown = (e: KeyboardEvent) => {
    if (e.nativeEvent.isComposing || e.key !== "Enter") return;
    e.preventDefault();
    const target = visible[cursor] ?? visible[0];
    if (target) pick(target);
  };

  /* Tab 等でフォーカスが外に出たら閉じる(外側 click は pointerdown が先に処理) */
  const onBlur = (e: FocusEvent) => {
    if (open && e.relatedTarget && !rootRef.current?.contains(e.relatedTarget as Node))
      setOpen(false);
  };

  return (
    <div className="fd" ref={rootRef} onBlur={onBlur} onKeyDown={onRootKeyDown}>
      <button
        ref={triggerRef}
        type="button"
        className={active ? "fd-trigger on" : "fd-trigger"}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        onClick={() => (open ? close(false) : openPopover())}
        onKeyDown={(e) => {
          if (!open && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
            e.preventDefault();
            openPopover();
          }
        }}
      >
        {dataKey}
        <svg className="fd-caret" viewBox="0 0 16 16" width="10" height="10" aria-hidden="true">
          <path
            d="M4 6.2 8 10l4-3.8"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
      {open && (
        /* tabIndex=-1: popover の余白クリックでフォーカスを body に逃がさない
         * (逃がすと root のキー処理が届かず Esc が Drawer に漏れる) */
        <div className="fd-pop" tabIndex={-1}>
          {searchable && (
            <div className="fd-search">
              <input
                ref={searchRef}
                type="text"
                value={query}
                placeholder={t`絞り込み…`}
                aria-label={t`${{ key: dataKey }} の選択肢を検索`}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setCursor(0);
                }}
                onKeyDown={onSearchKeyDown}
              />
            </div>
          )}
          <div role="listbox" id={listId} aria-label={ariaLabel} className="fd-list">
            {visible.map(([v, l], i) => (
              <button
                key={v}
                type="button"
                role="option"
                className="fd-opt"
                aria-selected={isActive([v, l])}
                tabIndex={i === cursor ? 0 : -1}
                ref={(el) => {
                  optionRefs.current[i] = el;
                }}
                onClick={() => pick([v, l])}
                onFocus={() => setCursor(i)} // click / Tab でのフォーカス移動も roving に反映
              >
                <svg
                  className="fd-check"
                  viewBox="0 0 16 16"
                  width="12"
                  height="12"
                  aria-hidden="true"
                >
                  <path
                    d="M3.2 8.6 6.6 12l6.2-7.4"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
                {l}
              </button>
            ))}
            {!visible.length && (
              <div className="fd-empty">
                <Trans>該当なし</Trans>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
});
