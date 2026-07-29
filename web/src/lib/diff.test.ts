import type { FileDiffMetadata } from "@pierre/diffs";
import { describe, expect, it } from "vitest";
import { makeDiffFile, makeDiffResponse } from "../test/fixtures";
import {
  diffMeta,
  diffTotals,
  diffWarning,
  MAX_FILE_RENDER_CHARS,
  MAX_FILE_RENDER_LINES,
  MAX_TOTAL_RENDER_CHARS,
  MAX_TOTAL_RENDER_LINES,
  parseDiffFiles,
  planFileRendering,
  renderedLineCount,
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

/* 行数 + 文字数予算 — サーバーの byte 上限では防げない敵性 patch の DOM
 * 爆発対策。planFileRendering は name / hunks[].unifiedLineCount /
 * additionLines / deletionLines しか読まない。 */
describe("planFileRendering", () => {
  const file = (name: string, hunkLines: number[], additionLines: string[] = []) =>
    ({
      name,
      hunks: hunkLines.map((n) => ({ unifiedLineCount: n })),
      additionLines,
      deletionLines: [],
    }) as unknown as FileDiffMetadata;

  it("per-file 行数上限を超える file だけを collapsed にする", () => {
    const plan = planFileRendering([
      file("small.ts", [10]),
      file("bomb.txt", [MAX_FILE_RENDER_LINES + 1]),
      file("small2.ts", [20]),
    ]);
    expect(plan.map((p) => p.overBudget)).toEqual([false, true, false]);
    expect(plan[1]!.lines).toBe(MAX_FILE_RENDER_LINES + 1);
  });

  it("行数が少なくても文字数上限を超える token 高密度 file は collapsed にする", () => {
    const dense = file("dense.ts", [260], ["x".repeat(MAX_FILE_RENDER_CHARS + 1)]);
    const plan = planFileRendering([file("small.ts", [10]), dense]);
    expect(plan.map((p) => p.overBudget)).toEqual([false, true]);
    expect(plan[1]!.chars).toBe(MAX_FILE_RENDER_CHARS + 1);
  });

  it("合計行数予算を使い切ったら以降の file を collapsed にする", () => {
    const big = MAX_FILE_RENDER_LINES; // 上限ちょうどは展開可
    const n = Math.floor(MAX_TOTAL_RENDER_LINES / big); // 予算を丁度使い切る本数
    const files = Array.from({ length: n + 1 }, (_, i) => file(`f${i}.ts`, [big]));
    files.push(file("tiny.ts", [1])); // 予算 0 では 1 行も超過
    const plan = planFileRendering(files);
    expect(plan.filter((p) => !p.overBudget)).toHaveLength(n);
    expect(plan.slice(n).map((p) => p.overBudget)).toEqual([true, true]);
  });

  it("合計文字数予算を使い切ったら以降の file を collapsed にする", () => {
    const half = "x".repeat(MAX_TOTAL_RENDER_CHARS / 2);
    const files = [
      file("a.ts", [10], [half]),
      file("b.ts", [10], [half]),
      file("c.ts", [10], ["y"]), // 文字数予算 0 では 1 文字も超過
    ];
    expect(planFileRendering(files).map((p) => p.overBudget)).toEqual([false, false, true]);
  });
});

describe("renderedLineCount", () => {
  it("ファイル後方の小さい hunk を絶対行位置で数えない(collapsedBefore 除外)", () => {
    // 6,000 行目の 1 行変更 — file 単位 unifiedLineCount は 6,001 相当に
    // なるが、描画されるのは hunk の 3 行だけ。誤って collapsed にしない。
    const patch = [
      "diff --git a/deep.ts b/deep.ts",
      "index 0123456..89abcde 100644",
      "--- a/deep.ts",
      "+++ b/deep.ts",
      "@@ -6000,3 +6000,3 @@",
      " ctx1",
      "-old",
      "+new",
      " ctx2",
      "",
    ].join("\n");
    const [f] = parseDiffFiles(patch);
    expect(f).toBeDefined();
    expect(renderedLineCount(f!)).toBeLessThanOrEqual(7);
    expect(planFileRendering([f!])[0]!.overBudget).toBe(false);
  });
});

describe("diffMeta", () => {
  it("merge-base 短縮 SHA・取得時刻・file 数・合計統計を並べる", () => {
    expect(diffMeta(makeDiffResponse())).toMatch(
      /^merge-base 0123456789 · captured \d{2}:\d{2}:\d{2} · 1 files \+1\/-1$/,
    );
  });
});
