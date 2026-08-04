// 複雑度しきい値の単一ソース (TypeScript 側)。hook (scripts/agent-complexity-on-edit.sh)
// と CI (.github/workflows/complexity.yml) はどちらもこの config を読む。数値をここ以外
// に書かないこと — ローカルで通るのに CI で落ちる、が必ず起きる。
//
// なぜ web/ 本体ではなくこの隔離パッケージに置くか: typescript-eslint は
// TypeScript 7.0 で "does not support TS 7.0" とハードエラーになる
// (typescript-eslint#10940)。web/ は tsc --noEmit のために typescript@7 を使い続ける
// ので、ESLint 一式だけを別 workspace パッケージに閉じて typescript@6 を直接依存に
// 持たせる。pnpm の overrides では peer 解決を変えられないため、パッケージ分離が唯一の
// 手段。typescript の依存を 7.x に上げると ESLint 全体が動かなくなる。
//
// 助言モード: FANOUT_COMPLEXITY_ADVISORY=1 で全しきい値が 2/3 になる。hook はブロック
// 判定と助言判定でこの config を 2 回読むだけで、しきい値を二重に持たない。
import sonarjs from "eslint-plugin-sonarjs";
import tseslint from "typescript-eslint";

const advisory = process.env.FANOUT_COMPLEXITY_ADVISORY === "1";

// t scales a block threshold down for the advisory pass. Floor, never below 1.
const t = (n) => (advisory ? Math.max(1, Math.floor((n * 2) / 3)) : n);

// 非製品コードは全層で同じ内容を除外する。正典の一覧は docs/complexity.ja.md の
// 「除外対象」表で、hook の eligible() と scripts/complexity-branch.sh がこれと同じ
// 集合を持つ。片方だけ増やさないこと。
//
// テスト: テーブル駆動テストと大きな describe ブロックは複雑度の対象外 —
// src/test/app.test.tsx の 1136 行 describe を叩いても得るものがない。vitest は
// 既定で .spec も収集する (vite.config.ts は test.include を上書きしない)。
// 残りは今このリポジトリに存在しないが、生まれた瞬間にゲートが誤爆しないよう
// 先に入れてある。
const EXCLUDED = [
  "src/**/*.test.ts",
  "src/**/*.test.tsx",
  "src/**/*.spec.ts",
  "src/**/*.spec.tsx",
  "src/test/**",
  "src/**/*.stories.ts",
  "src/**/*.stories.tsx",
  "src/**/__mocks__/**",
  "src/**/*.d.ts",
  "src/**/*.gen.ts",
  "src/**/*.gen.tsx",
  "src/**/generated/**",
];

// rules builds the metric set for one file kind. cognitive complexity is the primary
// metric; cyclomatic is the secondary one and max-depth carries the nesting dimension
// that cyclomatic ignores.
const rules = ({ cognitive, cyclomatic, lines, statements }) => ({
  "sonarjs/cognitive-complexity": ["error", t(cognitive)],
  "sonarjs/no-identical-functions": "error",
  complexity: ["error", t(cyclomatic)],
  "max-lines-per-function": ["error", t(lines)],
  "max-statements": ["error", t(statements)],
  "max-depth": ["error", t(3)],
  "max-params": ["error", t(3)],
  "max-nested-callbacks": ["error", t(3)],
});

const language = {
  parser: tseslint.parser,
  parserOptions: { ecmaFeatures: { jsx: true }, sourceType: "module" },
};

export default [
  {
    files: ["src/**/*.ts"],
    ignores: EXCLUDED,
    languageOptions: language,
    linterOptions: { reportUnusedDisableDirectives: "error" },
    plugins: { sonarjs },
    rules: rules({ cognitive: 7, cyclomatic: 8, lines: 60, statements: 10 }),
  },
  {
    // .tsx は循環的複雑度だけ緩める。JSX の && と三項演算子が分岐として機械的に
    // 数えられるため (実測 p99: .tsx 23 / .ts 13)。認知的複雑度は逆に .tsx のほうが
    // 低い (p99: 14 / 18) が、.tsx を .ts より厳しくすると運用が壊れるので p95 を採る。
    files: ["src/**/*.tsx"],
    ignores: EXCLUDED,
    languageOptions: language,
    linterOptions: { reportUnusedDisableDirectives: "error" },
    plugins: { sonarjs },
    rules: rules({ cognitive: 8, cyclomatic: 10, lines: 80, statements: 12 }),
  },
];
