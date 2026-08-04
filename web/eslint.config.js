// ESLint はこのリポジトリでは複雑度チェック専用。通常の lint は oxlint が担当する
// (pnpm run lint / make lint-web)。この config はしきい値を持たない — 実体は
// tools/complexity/eslint.config.js にあり、ここに置いてあるのは ESLint の base path を
// web/ にするため。base path が web/ でないと src/** パターンが解決できない。
export { default } from "fanout-complexity-lint";
