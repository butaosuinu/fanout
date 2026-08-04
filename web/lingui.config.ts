import { defineConfig } from "@lingui/cli";
import { formatter } from "@lingui/format-po";

// ソース文言は日本語。ja もカタログを生成してロードする — 本番ビルドでは macro が
// descriptor から message 本体を落とすことがあり(vite.config.ts の
// descriptorFields を参照)、ソースロケールにも実体が要るため。
export default defineConfig({
  sourceLocale: "ja",
  locales: ["ja", "en"],
  catalogs: [
    {
      path: "<rootDir>/src/locales/{locale}",
      include: ["src"],
      exclude: ["src/test/**", "**/*.test.ts", "**/*.test.tsx"],
    },
  ],
  // messageId 順に固定し、行番号は書かない。抽出結果を決定的にして、コードを
  // 動かしただけで .po 全体に差分が出るのを防ぐ(CI のカタログ drift 検査が
  // 実質的な変更だけを捉えられるようにするため)。ファイル参照は残す。
  orderBy: "messageId",
  format: formatter({ lineNumbers: false }),
});
