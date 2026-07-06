/// <reference types="vitest/config" />
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// outDir は Go 側の go:embed 対象 (internal/ui/dashboard/static)。成果物はコミット
// しない (.gitignore 済み、.gitkeep のみ track)。emptyOutDir:false で .gitkeep を
// 守る代わりに、残骸掃除は Makefile の build-web が行う。ファイル名はハッシュ
// なしの決定的な名前 — サーバが Cache-Control: no-store を付けるので stale の
// 心配はなく、Go 側 smoke テストがパスを固定検証できる。
export default defineConfig({
  plugins: [react()],
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
