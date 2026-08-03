/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import { lingui, linguiTransformerBabelPreset } from "@lingui/vite-plugin";
import babel from "@rolldown/plugin-babel";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// outDir は Go 側の go:embed 対象 (internal/ui/dashboard/static)。成果物はコミット
// しない (.gitignore 済み、.gitkeep のみ track)。emptyOutDir:false で .gitkeep を
// 守る代わりに、残骸掃除は Makefile の build-web が行う。ファイル名はハッシュ
// なしの決定的な名前 — サーバが Cache-Control: no-store を付けるので stale の
// 心配はなく、Go 側 smoke テストがパスを固定検証できる。
// lingui() は *.po を実行時カタログへ変換する (コンパイル済み catalog を repo に
// 置かずに済む)。macro (@lingui/core/macro, @lingui/react/macro) の変換は Babel が
// 要る — @vitejs/plugin-react v6 に babel オプションは無いので独立プラグインで足す。
// test ブロックも同じ plugins を継承するため、vitest でも macro が変換される。
//
// descriptorFields:"message" は既定の "auto" を上書きする。auto は本番ビルドだけ
// descriptor から message を落とすので、カタログが古いと日本語が base64 の ID
// (例 wKj0zc) として画面に出る。message を残せば、最悪でも「英語 UI に日本語が
// 混じる」に留まる — 数 KB とその安全性を交換する。
export default defineConfig({
  plugins: [
    react(),
    lingui({ failOnCompileError: true }),
    babel({ presets: [linguiTransformerBabelPreset({ descriptorFields: "message" })] }),
  ],
  build: {
    outDir: "../internal/ui/dashboard/static",
    emptyOutDir: false,
    rollupOptions: {
      input: { app: fileURLToPath(new URL("./index.html", import.meta.url)) },
      output: {
        entryFileNames: "assets/app.js",
        chunkFileNames: "assets/[name].js",
        assetFileNames: "assets/[name][extname]",
      },
    },
  },
  server: {
    // 開発時は実サーバへ proxy する:
    //   ./fanout-go dashboard --web --port 7777 --no-token
    // を起動してから `pnpm dev`。token 運用時は http://localhost:5173/?token=XXX
    // で開けば SPA が自分の URL から token を読んで /api/* に付与する。
    proxy: {
      "/api": {
        target: process.env.FANOUT_DASHBOARD_ORIGIN ?? "http://127.0.0.1:7777",
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["src/test/setup.ts"],
  },
});
