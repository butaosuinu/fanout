import type { FileDiffMetadata } from "@pierre/diffs";
import { describe, expect, it } from "vitest";
import { makeDiffFile, makeDiffResponse } from "../test/fixtures";
import {
  diffMeta,
  diffTotals,
  diffWarning,
  estimatedRenderNodes,
  FIXED_NODES_PER_FILE,
  MAX_EXPANDABLE_LINES,
  MAX_FILE_RENDER_NODES,
  MAX_TOTAL_RENDER_NODES,
  NODES_PER_CHAR,
  NODES_PER_CHAR_INLINE_DIFF,
  NODES_PER_LINE,
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

/* 最悪ケース DOM node 予算 — サーバーの byte 上限では防げない敵性 patch の
 * DOM 爆発対策。planFileRendering は name / hunks[].unifiedLineCount /
 * additionLines / deletionLines しか読まない。 */
describe("planFileRendering", () => {
  const file = (name: string, hunkLines: number[], additionLines: string[] = []) =>
    ({
      name,
      hunks: hunkLines.map((n) => ({ unifiedLineCount: n })),
      additionLines,
      deletionLines: [],
    }) as unknown as FileDiffMetadata;

  /* 行数だけで per-file 予算を超える形(改行のみの巨大ファイル) */
  it("行数由来のコストが per-file 予算を超える file だけを collapsed にする", () => {
    const lines = MAX_FILE_RENDER_NODES / NODES_PER_LINE + 1;
    const plan = planFileRendering([
      file("small.ts", [10]),
      file("bomb.txt", [lines]),
      file("small2.ts", [20]),
    ]);
    expect(plan.map((p) => p.overBudget)).toEqual([false, true, false]);
  });

  /* 文字数だけで per-file 予算を超える形(token 高密度・行数は少ない) */
  it("行数が少なくても文字数由来のコストが予算を超える file は collapsed にする", () => {
    const chars = MAX_FILE_RENDER_NODES / NODES_PER_CHAR + 1;
    const dense = file("dense.ts", [260], ["x".repeat(chars)]);
    const plan = planFileRendering([file("small.ts", [10]), dense]);
    expect(plan.map((p) => p.overBudget)).toEqual([false, true]);
    expect(plan[1]!.nodes).toBeGreaterThan(MAX_FILE_RENDER_NODES);
  });

  /* inline diff の decoration を足すと超えるだけの file は、collapsed に
   * せず inline diff だけ切って highlight を残す(2 段階の 1 段目) */
  it("inline diff の分だけ予算を超える file は inline diff だけ切って展開する", () => {
    const chars = MAX_FILE_RENDER_NODES / (NODES_PER_CHAR + NODES_PER_CHAR_INLINE_DIFF) + 100;
    const f = file("mid.ts", [200], ["x".repeat(chars)]);
    expect(estimatedRenderNodes(f, true)).toBeGreaterThan(MAX_FILE_RENDER_NODES);
    expect(estimatedRenderNodes(f)).toBeLessThanOrEqual(MAX_FILE_RENDER_NODES);
    const [entry] = planFileRendering([f]);
    expect(entry).toMatchObject({ overBudget: false, inlineDiff: false });
  });

  it("per-file 予算内でも合計予算の残りに収まらない file は collapsed にする", () => {
    /* 1 file あたり per-file 予算いっぱい — 2 file 目は合計予算の残りに入らない。
     * 予算は残量方式なので、超過 file の後でも残りに収まる小さい file は描画する
     * (1 つ大きい file があるだけで以降すべてを畳まない)。 */
    const chars = "x".repeat((MAX_FILE_RENDER_NODES - FIXED_NODES_PER_FILE) / NODES_PER_CHAR);
    const files = [file("a.ts", [], [chars]), file("b.ts", [], [chars]), file("c.ts", [], ["y"])];
    const plan = planFileRendering(files);
    expect(plan.map((p) => p.overBudget)).toEqual([false, true, false]);
    expect(MAX_FILE_RENDER_NODES * 2).toBeGreaterThan(MAX_TOTAL_RENDER_NODES);
  });

  /* 展開しても行数だけで固まる大きさ(契約内で 26 万行が作れる) */
  it("展開可能行数の上限を超える file は展開自体を許さない", () => {
    const plan = planFileRendering([
      file("huge.txt", [MAX_EXPANDABLE_LINES + 1]),
      file("big.txt", [MAX_EXPANDABLE_LINES]),
    ]);
    expect(plan.map((p) => p.overBudget)).toEqual([true, true]);
    expect(plan.map((p) => p.tooLargeToExpand)).toEqual([true, false]);
  });

  it("典型的なレビュー diff(300 行 × 60 文字)は展開したまま描画する", () => {
    const f = file(
      "normal.ts",
      [300],
      Array.from({ length: 300 }, () => "x".repeat(60)),
    );
    const plan = planFileRendering([f]);
    expect(plan[0]!.overBudget).toBe(false);
    // 実測 37,312 node — 見積りは同程度に収まる
    expect(estimatedRenderNodes(f)).toBeLessThanOrEqual(MAX_FILE_RENDER_NODES);
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
