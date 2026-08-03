import type { ReactNode } from "react";

/* アイコンだけのボタン。ラベルは読み上げ用の aria-label と、CSS で出す
 * ツールチップ(data-tip)の両方に同じ文字列を渡す。title は使わない —
 * ネイティブのツールチップが重なって 2 個出るため。 */
export function IconButton({
  id,
  className,
  label,
  popup,
  disabled,
  onClick,
  children,
}: {
  id?: string;
  className?: string;
  label: string;
  popup?: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      id={id}
      className={className ? `icon-btn tip ${className}` : "icon-btn tip"}
      data-tip={label}
      aria-label={label}
      aria-haspopup={popup ? "dialog" : undefined}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

/* diff ビュアーのアイコンボタン用。Nav の歯車と同じ描き味(24 グリッド・
 * stroke currentColor・丸端)に揃える。ラベルはボタン側の aria-label が持つので、
 * ここは常に aria-hidden。 */
function Glyph({ children }: { children: ReactNode }) {
  return (
    <svg
      width="15"
      height="15"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  );
}

export function IconRefresh() {
  return (
    <Glyph>
      <path d="M20.5 12a8.5 8.5 0 1 1-2.49-6.01" />
      <path d="M20.5 4v5h-5" />
    </Glyph>
  );
}

/* 全画面 -> コンパクト。斜め内向きの矢印(縮める) */
export function IconMinimize() {
  return (
    <Glyph>
      <path d="M14.5 9.5h5M14.5 9.5v-5M14.5 9.5L21 3" />
      <path d="M9.5 14.5h-5M9.5 14.5v5M9.5 14.5L3 21" />
    </Glyph>
  );
}

/* コンパクト -> 全画面。斜め外向きの矢印(広げる) */
export function IconMaximize() {
  return (
    <Glyph>
      <path d="M15 3.5h5.5V9M20.5 3.5L14 10" />
      <path d="M9 20.5H3.5V15M3.5 20.5L10 14" />
    </Glyph>
  );
}

/* テーマ設定。明暗を半分ずつ塗ったコントラスト円 */
export function IconTheme() {
  return (
    <Glyph>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 3.5a8.5 8.5 0 0 1 0 17Z" fill="currentColor" stroke="none" />
    </Glyph>
  );
}

export function IconClose() {
  return (
    <Glyph>
      <path d="M6.5 6.5l11 11M17.5 6.5l-11 11" />
    </Glyph>
  );
}

/* すべて展開。上下へ開く矢印 */
export function IconUnfold() {
  return (
    <Glyph>
      <path d="M12 10.5v-7M8.5 7L12 3.5 15.5 7" />
      <path d="M12 13.5v7M8.5 17L12 20.5 15.5 17" />
    </Glyph>
  );
}

/* すべて折りたたむ。中央へ閉じる矢印 */
export function IconFold() {
  return (
    <Glyph>
      <path d="M12 3.5v7M8.5 7L12 10.5 15.5 7" />
      <path d="M12 20.5v-7M8.5 17L12 13.5 15.5 17" />
    </Glyph>
  );
}

/* 差分の並べ方。枠を縦線で割れば左右 2 面、横線で割れば縦積み。
 * auto は「幅で決まる」ことを破線の仕切りで示す。 */
export function IconLayoutSplit() {
  return (
    <Glyph>
      <rect x="3.5" y="5" width="17" height="14" rx="2.5" />
      <path d="M12 5v14" />
    </Glyph>
  );
}

export function IconLayoutStack() {
  return (
    <Glyph>
      <rect x="3.5" y="5" width="17" height="14" rx="2.5" />
      <path d="M3.5 12h17" />
    </Glyph>
  );
}

/* auto は仕切りを破線にして「幅で決まる」ことを示しつつ、向きは今そうなっている
 * ほうに合わせる — 破線だけだと split との差が 15px では弱い。 */
export function IconLayoutAuto({ stack }: { stack: boolean }) {
  return (
    <Glyph>
      <rect x="3.5" y="5" width="17" height="14" rx="2.5" />
      <path d={stack ? "M3.5 12h17" : "M12 5v14"} strokeDasharray="3 3" />
    </Glyph>
  );
}

export function IconChevronDown() {
  return (
    <Glyph>
      <path d="M6 9.5l6 6 6-6" />
    </Glyph>
  );
}

export function IconChevronUp() {
  return (
    <Glyph>
      <path d="M6 14.5l6-6 6 6" />
    </Glyph>
  );
}
