import { useEffect, useRef, useState, type FocusEvent, type KeyboardEvent } from "react";

export type Option = readonly [value: string, label: string];

/* GitHub PR 風フィルタードロップダウン(8 キー共通)。trigger ボタン +
 * role="listbox" の popover で、選択は onPickToken による key:value トークン
 * 書込のみ — 「#filter のテキストが単一の真実」という既存モデルは変えない。
 * アクティブ値の再クリックは onRemoveToken(トグルオフ)。
 *
 * - 開いている間は選択肢を凍結する: 2 秒 tick の snapshot 更新で開いたメニュー
 *   がズレたり閉じたりしない(旧 DynamicSelect の focus-freeze の置き換え)。
 * - Esc は popover を閉じて trigger にフォーカスを戻し、stopPropagation で
 *   Drawer の document keydown へ漏らさない(drawer まで閉じない)。
 * - option は flat button の roving tabindex(ArrowUp/Down で巡回、Enter で
 *   選択)。searchable(agent / wave)は検索 input から ↓ で option 列に入る。 */
export function FilterDropdown({
  dataKey,
  ariaLabel,
  placeholder,
  options,
  active,
  searchable = false,
  onPickToken,
  onRemoveToken,
}: {
  dataKey: string;
  ariaLabel: string;
  placeholder: string;
  options: Option[];
  active: { raw: string; value: string } | null;
  searchable?: boolean;
  onPickToken: (key: string, value: string) => void;
  onRemoveToken: (tok: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0); // roving tabindex の現在位置(visible 基準)
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const frozen = useRef(options);

  const base = open ? frozen.current : options;
  const q = query.trim().toLowerCase();
  const visible =
    searchable && q
      ? base.filter(([v, l]) => v.toLowerCase().includes(q) || l.toLowerCase().includes(q))
      : base;

  const openPopover = () => {
    frozen.current = options; // 開く瞬間の選択肢で凍結
    setQuery("");
    const idx = active ? options.findIndex(([v]) => v.toLowerCase() === active.value) : -1;
    setCursor(idx >= 0 ? idx : 0);
    setOpen(true);
  };
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) triggerRef.current?.focus();
  };
  const pick = (value: string) => {
    if (active && active.value === value.toLowerCase()) onRemoveToken(active.raw);
    else onPickToken(dataKey, value);
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

  const focusOption = (idx: number) => {
    setCursor(idx);
    optionRefs.current[idx]?.focus();
  };
  const moveCursor = (delta: 1 | -1) => {
    if (!visible.length) return;
    // 検索 input からの矢印キーは端から option 列に入る(↓=先頭 / ↑=末尾)
    const from = document.activeElement === searchRef.current ? (delta === 1 ? -1 : 0) : cursor;
    focusOption((from + delta + visible.length) % visible.length);
  };

  const onPopoverKeyDown = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation(); // Drawer の document keydown に漏らさない
      close(true);
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      moveCursor(e.key === "ArrowDown" ? 1 : -1);
    } else if ((e.key === "Home" || e.key === "End") && document.activeElement !== searchRef.current) {
      e.preventDefault();
      if (visible.length) focusOption(e.key === "Home" ? 0 : visible.length - 1);
    }
  };

  /* 検索 input の Enter は先頭の絞り込み結果を選択(GitHub 同様) */
  const onSearchKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const first = visible[0];
    if (first) pick(first[0]);
  };

  /* Tab 等でフォーカスが外に出たら閉じる(外側 click は pointerdown が先に処理) */
  const onBlur = (e: FocusEvent) => {
    if (open && e.relatedTarget && !rootRef.current?.contains(e.relatedTarget as Node)) setOpen(false);
  };

  optionRefs.current.length = visible.length;
  return (
    <div className="fd" ref={rootRef} onBlur={onBlur}>
      <button
        ref={triggerRef}
        type="button"
        className={active ? "fd-trigger on" : "fd-trigger"}
        data-key={dataKey}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => (open ? close(false) : openPopover())}
        onKeyDown={(e) => {
          if (!open && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
            e.preventDefault();
            openPopover();
          }
        }}
      >
        {placeholder}
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
        <div className="fd-pop" onKeyDown={onPopoverKeyDown}>
          {searchable && (
            <div className="fd-search">
              <input
                ref={searchRef}
                type="text"
                value={query}
                placeholder="絞り込み…"
                aria-label={`${placeholder} の選択肢を検索`}
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
          <div role="listbox" aria-label={ariaLabel} className="fd-list">
            {visible.map(([v, l], i) => (
              <button
                key={v}
                type="button"
                role="option"
                className="fd-opt"
                aria-selected={active?.value === v.toLowerCase()}
                tabIndex={i === cursor ? 0 : -1}
                ref={(el) => {
                  optionRefs.current[i] = el;
                }}
                onClick={() => pick(v)}
                onFocus={() => setCursor(i)} // click / Tab でのフォーカス移動も roving に反映
              >
                <svg className="fd-check" viewBox="0 0 16 16" width="12" height="12" aria-hidden="true">
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
            {!visible.length && <div className="fd-empty">該当なし</div>}
          </div>
        </div>
      )}
    </div>
  );
}
