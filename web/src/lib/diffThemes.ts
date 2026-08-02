/* diff ビュアー(@pierre/diffs)の syntax highlight テーマ。
 *
 * ライブラリは未登録のテーマ名を @pierre/theming の shiki カタログから動的
 * import() で解決するので、ここは名前の文字列だけを持てばよく、テーマ本体は
 * 選ばれたものだけが遅延 chunk として読み込まれる。ただし未知の名前は
 * resolveTheme が throw するため、localStorage から読んだ値は必ずこの
 * allowlist で検証してから渡す。 */

export interface DiffThemeOption {
  name: string;
  label: string;
}

export const DEFAULT_DIFF_THEME_LIGHT = "pierre-light";
export const DEFAULT_DIFF_THEME_DARK = "pierre-dark";

export const DIFF_THEMES_LIGHT: readonly DiffThemeOption[] = [
  { name: "pierre-light", label: "Pierre Light" },
  { name: "github-light", label: "GitHub Light" },
  { name: "catppuccin-latte", label: "Catppuccin Latte" },
  { name: "gruvbox-light-medium", label: "Gruvbox Light" },
  { name: "one-light", label: "One Light" },
  { name: "vitesse-light", label: "Vitesse Light" },
  { name: "solarized-light", label: "Solarized Light" },
  { name: "everforest-light", label: "Everforest Light" },
  { name: "kanagawa-lotus", label: "Kanagawa Lotus" },
];

export const DIFF_THEMES_DARK: readonly DiffThemeOption[] = [
  { name: "pierre-dark", label: "Pierre Dark" },
  { name: "github-dark", label: "GitHub Dark" },
  { name: "catppuccin-mocha", label: "Catppuccin Mocha" },
  { name: "gruvbox-dark-medium", label: "Gruvbox Dark" },
  { name: "one-dark-pro", label: "One Dark Pro" },
  { name: "vitesse-dark", label: "Vitesse Dark" },
  { name: "solarized-dark", label: "Solarized Dark" },
  { name: "tokyo-night", label: "Tokyo Night" },
  { name: "kanagawa-wave", label: "Kanagawa Wave" },
];

const LIGHT_NAMES = new Set(DIFF_THEMES_LIGHT.map((t) => t.name));
const DARK_NAMES = new Set(DIFF_THEMES_DARK.map((t) => t.name));

export function normalizeDiffTheme(name: string | null, dark: boolean): string {
  const known = dark ? DARK_NAMES : LIGHT_NAMES;
  if (name && known.has(name)) return name;
  return dark ? DEFAULT_DIFF_THEME_DARK : DEFAULT_DIFF_THEME_LIGHT;
}

/* 設定モーダルのテーマ見本。実物の <FileDiff> にそのまま食わせるので、見本と
 * 本番の配色・行の見た目が必ず一致する。ここに置くのは固定リテラルであり、
 * patch 由来の敵性入力ではない(見本に外部データを混ぜないこと)。
 * コメント・キーワード・文字列・数値が 1 行ずつ乗るよう内容を選んである。
 * 幅の狭い 2 カラムに横スクロール無しで収まるよう、1 行は 22 文字までに保つこと。 */
export const DIFF_THEME_SAMPLE_PATCH = `diff --git a/theme-sample.ts b/theme-sample.ts
--- a/theme-sample.ts
+++ b/theme-sample.ts
@@ -1,5 +1,5 @@
 // diff テーマ
 const theme = {
-  accent: "#165E83",
+  accent: "#00A3AF",
   lines: 42,
 };
`;
