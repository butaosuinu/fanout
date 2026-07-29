import type { FileDiffMetadata } from "@pierre/diffs";
import { describe, expect, it } from "vitest";
import { makeDiffFile, makeDiffResponse } from "../test/fixtures";
import {
  diffMeta,
  diffTotals,
  diffWarning,
  MAX_FILE_RENDER_LINES,
  MAX_TOTAL_RENDER_LINES,
  parseDiffFiles,
  partitionRenderableFiles,
} from "./diff";

describe("parseDiffFiles", () => {
  it("連結 patch をライブラリのパーサで file 単位に分解する", () => {
    const patch = [
      "diff --git a/a.ts b/a.ts",
      "--- a/a.ts",
      "+++ b/a.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "diff --git a/b.ts b/b.ts",
      "--- a/b.ts",
      "+++ b/b.ts",
      "@@ -1 +1 @@",
      "-x",
      "+y",
      "",
    ].join("\n");
    const files = parseDiffFiles(patch);
    expect(files.map((f) => f.name)).toEqual(["a.ts", "b.ts"]);
  });

  it("空 patch は空配列", () => {
    expect(parseDiffFiles("")).toEqual([]);
  });
});

describe("diffWarning", () => {
  it("完全な patch では警告しない", () => {
    expect(diffWarning(makeDiffResponse())).toBeNull();
  });

  it("truncated なら省略件数なしでも警告する", () => {
    expect(diffWarning(makeDiffResponse({ truncated: true }))).toMatch(/揃っていません/);
  });

  it("patchIncluded=false の file があれば件数付きで警告する", () => {
    const d = makeDiffResponse({
      files: [
        makeDiffFile(),
        makeDiffFile({
          path: "big.bin",
          binary: true,
          patchIncluded: false,
          omittedReason: "binary",
        }),
      ],
    });
    expect(diffWarning(d)).toMatch(/1 file/);
  });
});

describe("diffTotals", () => {
  it("collectionLimit の null 統計は数えず合計する", () => {
    const { additions, deletions } = diffTotals([
      makeDiffFile({ additions: 3, deletions: 1 }),
      makeDiffFile({
        path: "skipped.ts",
        additions: null,
        deletions: null,
        patchIncluded: false,
        omittedReason: "collectionLimit",
      }),
    ]);
    expect(additions).toBe(3);
    expect(deletions).toBe(1);
  });
});

/* 行数上限 — サーバーの byte 上限では防げない敵性 patch の DOM 爆発対策。
 * partitionRenderableFiles は name / unifiedLineCount しか読まない。 */
describe("partitionRenderableFiles", () => {
  const file = (name: string, unifiedLineCount: number) =>
    ({ name, unifiedLineCount }) as FileDiffMetadata;

  it("per-file 上限を超える file は省略し、残りは描画する", () => {
    const { rendered, suppressed } = partitionRenderableFiles([
      file("small.ts", 10),
      file("bomb.txt", MAX_FILE_RENDER_LINES + 1),
      file("small2.ts", 20),
    ]);
    expect(rendered.map((f) => f.name)).toEqual(["small.ts", "small2.ts"]);
    expect(suppressed).toEqual([{ name: "bomb.txt", lines: MAX_FILE_RENDER_LINES + 1 }]);
  });

  it("合計予算を使い切ったら以降の大きい file を省略し、収まる file は描画する", () => {
    const big = MAX_FILE_RENDER_LINES; // 上限ちょうどは描画可
    const n = Math.floor(MAX_TOTAL_RENDER_LINES / big); // 予算を丁度使い切る本数
    const files = Array.from({ length: n + 1 }, (_, i) => file(`f${i}.ts`, big));
    files.push(file("tiny.ts", 1)); // 予算 0 でも 1 行は超過なので省略される
    const { rendered, suppressed } = partitionRenderableFiles(files);
    expect(rendered).toHaveLength(n);
    expect(suppressed.map((f) => f.name)).toEqual([`f${n}.ts`, "tiny.ts"]);
  });
});

describe("diffMeta", () => {
  it("merge-base 短縮 SHA・取得時刻・file 数・合計統計を並べる", () => {
    expect(diffMeta(makeDiffResponse())).toMatch(
      /^merge-base 0123456789 · captured \d{2}:\d{2}:\d{2} · 1 files \+1\/-1$/,
    );
  });
});
