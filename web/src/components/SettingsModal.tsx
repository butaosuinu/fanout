import { lazy, Suspense, useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useAppearance, useDiffTheme, type Appearance, type Theme } from "../hooks/useSettings";
import { DIFF_THEMES_DARK, DIFF_THEMES_LIGHT, type DiffThemeOption } from "../lib/diffThemes";

/* 見本は実物の <FileDiff> で描く = @pierre/diffs(Shiki 込み)を引く。設定を
 * 開くまで初回ロードのパスに乗せないため、DiffOverlay と同じく遅延 chunk へ。 */
const DiffThemePreview = lazy(() => import("./DiffThemePreview"));

const APPEARANCES: { value: Appearance; label: string }[] = [
  { value: "system", label: "システム" },
  { value: "light", label: "ライト" },
  { value: "dark", label: "ダーク" },
];

/* 外観の見本。ダッシュボードの縮図を CSS だけで描く(nav の帯 + カード + 行)。
 * 色はページ変数ではなくパレットの実値を CSS 側で直に置いているので、いま
 * どちらの外観でいても light / dark の見た目がそのまま並ぶ。system は同じ絵を
 * 2 枚重ねて右半分だけ dark を出す。 */
function AppearanceArt() {
  return (
    <span className="stc-art" aria-hidden="true">
      <span className="stc-face stc-light">
        <span className="stc-win">
          <i className="stc-bar" />
          <i className="stc-l w1" />
          <i className="stc-l w2" />
          <i className="stc-l w3" />
        </span>
      </span>
      <span className="stc-face stc-dark">
        <span className="stc-win">
          <i className="stc-bar" />
          <i className="stc-l w1" />
          <i className="stc-l w2" />
          <i className="stc-l w3" />
        </span>
      </span>
    </span>
  );
}

function ThemeSelect({
  id,
  label,
  value,
  themeType,
  options,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  themeType: Theme;
  options: readonly DiffThemeOption[];
  onChange: (name: string) => void;
}) {
  return (
    <div className="set-diff-theme">
      {/* fallback はテーマ読込中の高さ確保。空の枠が一瞬出るだけで文言は出さない */}
      <Suspense fallback={<div className="set-theme-preview is-loading" />}>
        <DiffThemePreview name={value} themeType={themeType} />
      </Suspense>
      <label htmlFor={id}>{label}</label>
      <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        {options.map((t) => (
          <option key={t.name} value={t.name}>
            {t.label}
          </option>
        ))}
      </select>
    </div>
  );
}

export function SettingsModal({
  onClose,
  onClosed,
}: {
  onClose: () => void;
  /* unmount 時、背面の inert 解除「後」に呼ぶ(起点へのフォーカス復帰用) */
  onClosed?: () => void;
}) {
  const { mode, setMode } = useAppearance();
  const { light, dark, setLight, setDark } = useDiffTheme();
  const rootRef = useRef<HTMLDivElement>(null);
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;

  /* diff オーバーレイと同じモーダル作法: 初期フォーカスを移し、背面を inert に
   * して遮る。diff オーバーレイは #root の外(portal)なので個別に inert する。
   * 解除してから onClosed を呼ぶ(inert な subtree への focus は実ブラウザで
   * 拒否されるため順序が本質)。 */
  useEffect(() => {
    rootRef.current?.focus();
    /* 自分が付けた inert だけを外す。全画面 diff から開いた場合 #root は既に
     * DiffOverlay が inert にしており、無条件に外すと diff が全面を覆ったまま
     * 背面へ Tab できるようになる(DiffOverlay の effect は再実行されない)。 */
    const blocked = [document.getElementById("root"), document.getElementById("diff-overlay")];
    const added = blocked.filter((el) => el != null && !el.hasAttribute("inert")) as HTMLElement[];
    for (const el of added) el.setAttribute("inert", "");
    return () => {
      for (const el of added) el.removeAttribute("inert");
      onClosedRef.current?.();
    };
  }, []);

  /* Escape は capture 段で受けて preventDefault する。下に diff オーバーレイが
   * 開いていても、そちらは escapeEnabled=false で降りているので閉じない。 */
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) {
        e.preventDefault();
        onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [onClose]);

  return createPortal(
    <div className="settings-backdrop" onClick={onClose}>
      <div
        className="settings-modal"
        id="settings-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="settings-title"
        ref={rootRef}
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="settings-head">
          <h3 id="settings-title">設定</h3>
          <button type="button" id="settings-close" aria-label="設定を閉じる" onClick={onClose}>
            ✕
          </button>
        </header>
        <section className="set-sec">
          {/* fieldset/legend は legend が flex/grid に乗らず折り返しが崩れるので、
              radiogroup + aria-labelledby で同じ意味論を作る */}
          <h4 id="set-appearance-label">テーマ</h4>
          <div className="set-theme-cards" role="radiogroup" aria-labelledby="set-appearance-label">
            {APPEARANCES.map((a) => (
              <label key={a.value} className="set-theme-card" data-mode={a.value}>
                <input
                  type="radio"
                  name="appearance"
                  value={a.value}
                  checked={mode === a.value}
                  onChange={() => setMode(a.value)}
                />
                <AppearanceArt />
                <span className="stc-label">{a.label}</span>
              </label>
            ))}
          </div>
        </section>
        <section className="set-sec">
          <h4>diff テーマ</h4>
          <div className="set-diff-themes">
            <ThemeSelect
              id="set-diff-theme-light"
              label="ライトテーマ"
              value={light}
              themeType="light"
              options={DIFF_THEMES_LIGHT}
              onChange={setLight}
            />
            <ThemeSelect
              id="set-diff-theme-dark"
              label="ダークテーマ"
              value={dark}
              themeType="dark"
              options={DIFF_THEMES_DARK}
              onChange={setDark}
            />
          </div>
        </section>
      </div>
    </div>,
    document.body,
  );
}
