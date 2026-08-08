import { i18n } from "@lingui/core";
import type { FileDiffMetadata } from "@pierre/diffs";
import { describe, expect, it } from "vitest";
import { makeDiffFile, makeDiffResponse } from "../../test/fixtures";
import {
  COLLAPSE_LINE_THRESHOLD,
  diffMeta,
  diffTotals,
  diffWarning,
  groupDiffFilesByDir,
  HIGHLIGHT_MAX_CHARS,
  HIGHLIGHT_MAX_LINES,
  indexDiffFilesByPath,
  indexDiffKindsByPath,
  INLINE_DIFF_MAX_CHARS,
  parseDiffFiles,
  planDiffFiles,
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

  /* サイドバーは files[].path(= 移動先)で本文を引く。ライブラリが name に
   * どちらを入れるかが変わると、移動した file にだけ飛べなくなる。 */
  it("rename は移動先を name、移動元を prevName に持つ", () => {
    const patch = [
      "diff --git a/src/old.ts b/src/new.ts",
      "similarity index 87%",
      "rename from src/old.ts",
      "rename to src/new.ts",
      "--- a/src/old.ts",
      "+++ b/src/new.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "",
    ].join("\n");
    const [file] = parseDiffFiles(patch);
    expect(file?.name).toBe("src/new.ts");
    expect(file?.prevName).toBe("src/old.ts");
  });

  /* hunk を持たない pure rename が 1 entry として残らないと、patchIncluded な
   * file が本文から黙って消える(サーバーは 1 block として数えている)。 */
  it("hunk のない pure rename も 1 entry になる", () => {
    const patch = [
      "diff --git a/src/old.ts b/src/new.ts",
      "similarity index 100%",
      "rename from src/old.ts",
      "rename to src/new.ts",
      "",
    ].join("\n");
    expect(parseDiffFiles(patch).map((f) => f.name)).toEqual(["src/new.ts"]);
  });
});

describe("diffWarning", () => {
  it("完全な patch では警告しない", () => {
    expect(diffWarning(makeDiffResponse())).toBeNull();
  });

  it("truncated なら省略件数なしでも警告する", () => {
    expect(i18n._(diffWarning(makeDiffResponse({ truncated: true }))!)).toMatch(/揃っていません/);
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
    expect(i18n._(diffWarning(d)!)).toMatch(/1 file/);
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

/* 描画方針。総量の有界性は仮想化が担うので、ここは file 単位の閾値だけを見る。
 * planDiffFiles は name / hunks[].unifiedLineCount / additionLines /
 * deletionLines しか読まない。 */
describe("planDiffFiles", () => {
  const file = (name: string, hunkLines: number[], additionLines: string[] = []) =>
    ({
      name,
      hunks: hunkLines.map((n) => ({ unifiedLineCount: n })),
      additionLines,
      deletionLines: [],
    }) as unknown as FileDiffMetadata;

  it("典型的なレビュー diff は何 file 並んでも全部展開したままにする", () => {
    const files = Array.from({ length: 20 }, (_, i) =>
      file(
        `f${i}.ts`,
        [200],
        Array.from({ length: 200 }, () => "x".repeat(60)),
      ),
    );
    const plan = planDiffFiles(files);
    expect(plan.every((p) => !p.initiallyCollapsed)).toBe(true);
    expect(plan.every((p) => p.highlight)).toBe(true);
  });

  it("折りたたみ閾値ちょうどの行数から折りたたむ", () => {
    const plan = planDiffFiles([
      file("just-under.ts", [COLLAPSE_LINE_THRESHOLD - 1]),
      file("at.ts", [COLLAPSE_LINE_THRESHOLD]),
    ]);
    expect(plan.map((p) => p.initiallyCollapsed)).toEqual([false, true]);
  });

  it("折りたたんだ file でも highlight は落とさない", () => {
    const [p] = planDiffFiles([
      file(
        "big.ts",
        [2_000],
        Array.from({ length: 2_000 }, () => "x".repeat(40)),
      ),
    ]);
    expect(p).toMatchObject({ initiallyCollapsed: true, highlight: true });
  });

  /* トークン化は描画範囲ではなく file 全体に走るので、短い行が大量にある file は
   * 文字数だけでは止まらない(74,000 行の `x` は 74,000 文字で文字数上限を通り、
   * ライブラリ自身の tokenizeMaxLength = 100,000 行にも掛からない)。 */
  it("文字数は少なくても行数が多い file は highlight を切る", () => {
    const many = file(
      "many.ts",
      [HIGHLIGHT_MAX_LINES + 1],
      Array.from({ length: HIGHLIGHT_MAX_LINES + 1 }, () => "x"),
    );
    const [plan] = planDiffFiles([many]);
    expect(plan?.chars).toBeLessThan(HIGHLIGHT_MAX_CHARS); // 文字数側は素通りする
    expect(plan?.highlight).toBe(false);
  });

  it("行数が少なくても内容量が多い file は highlight と行内差分を切る", () => {
    // 66 行 × 990 文字級の高密度 patch — 行数では測れない
    const dense = file("dense.ts", [66], ["x".repeat(HIGHLIGHT_MAX_CHARS + 1)]);
    const [p] = planDiffFiles([dense]);
    expect(p).toMatchObject({ initiallyCollapsed: false, highlight: false, inlineDiff: false });
  });

  it("highlight は残せるが行内差分だけ切る中間帯がある", () => {
    const mid = file("mid.ts", [500], ["x".repeat(INLINE_DIFF_MAX_CHARS + 1)]);
    const [p] = planDiffFiles([mid]);
    expect(p).toMatchObject({ highlight: true, inlineDiff: false });
  });
});

describe("groupDiffFilesByDir", () => {
  it("path 順で連続しない同一ディレクトリも 1 グループにまとめる", () => {
    // byte 順では a/b.ts < a/c/d.ts < a/e.ts で a/ が分断される
    const files = [
      makeDiffFile({ path: "a/b.ts" }),
      makeDiffFile({ path: "a/c/d.ts" }),
      makeDiffFile({ path: "a/e.ts" }),
      makeDiffFile({ path: "top.md" }),
    ];
    expect(groupDiffFilesByDir(files).map((g) => [g.dir, g.files.map((f) => f.path)])).toEqual([
      ["a/", ["a/b.ts", "a/e.ts"]],
      ["a/c/", ["a/c/d.ts"]],
      ["", ["top.md"]],
    ]);
  });
});

describe("不正 UTF-8 を含む path", () => {
  /* Go(encoding/json)は不正 byte を 1 個ずつ U+FFFD にするが、WHATWG の
   * TextDecoder は不正な列をまとめて 1 個にする。素の TextDecoder で復号すると
   * files[].path と key が食い違い、その file だけサイドバーから飛べなくなる。 */
  it("不正 byte 1 個につき 1 個の U+FFFD にする(Go と同じ規則)", () => {
    // \341\200 = 3 byte 列の頭 + 継続 1 byte(不足)。Go は 2 個、TextDecoder は 1 個
    const files = [{ name: "docs/\\341\\200.md" }] as unknown as FileDiffMetadata[];
    const [key] = [...indexDiffFilesByPath(files).keys()];
    expect(key).toBe("docs/\uFFFD\uFFFD.md");
    expect(new TextDecoder().decode(Uint8Array.from([0xe1, 0x80]))).toBe("\uFFFD"); // 参考: 素だと 1 個
  });

  it("正当な多 byte 列はそのまま復号する", () => {
    const files = [{ name: "docs/\\346\\227\\245.md" }] as unknown as FileDiffMetadata[];
    expect([...indexDiffFilesByPath(files).keys()]).toEqual(["docs/日.md"]);
  });
});

describe("indexDiffFilesByPath", () => {
  it("file type change の 2 entry を同じ path にまとめる", () => {
    const files = [
      { name: "a.ts" },
      { name: "swap" },
      { name: "swap" },
    ] as unknown as FileDiffMetadata[];
    expect(indexDiffFilesByPath(files)).toEqual(
      new Map([
        ["a.ts", [0]],
        ["swap", [1, 2]],
      ]),
    );
  });

  /* git は core.quotePath(既定 on)のとき patch 中の path を C 形式でエスケープ
   * するが、サーバーの files[].path は生のまま。key を正規化しないと非 ASCII の
   * ファイルがサイドバーから飛べなくなる。
   *
   * 実パーサを通すこと。parsePatchFiles は外側の `"` だけ剥がしてエスケープは
   * 残すので、引用符付きの文字列を直に食わせるテストだと実際の入力とずれる
   * (それで一度この正規化が丸ごと効いていなかった)。 */
  it("非 ASCII の quoted path を、実パーサ出力から生のパスへ戻す", () => {
    const quoted = '"a/docs/\\346\\227\\245\\346\\234\\254\\350\\252\\236.md"';
    const patch = [
      `diff --git ${quoted} ${quoted.replace("a/", "b/")}`,
      `--- ${quoted}`,
      `+++ ${quoted.replace("a/", "b/")}`,
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "",
    ].join("\n");
    expect([...indexDiffFilesByPath(parseDiffFiles(patch)).keys()]).toEqual(["docs/日本語.md"]);
  });

  it("エスケープの復号は引用符の有無に依存しない", () => {
    const files = [
      // parsePatchFiles が外側の " を剥がした後の形
      { name: "docs/\\346\\227\\245\\346\\234\\254\\350\\252\\236.md" },
      { name: '"a\\tb.txt"' },
      { name: "plain.ts" },
    ] as unknown as FileDiffMetadata[];
    expect([...indexDiffFilesByPath(files).keys()]).toEqual([
      "docs/日本語.md",
      "a\tb.txt",
      "plain.ts",
    ]);
  });

  it("解釈できないエスケープを含む name はそのまま扱う", () => {
    const files = [
      { name: "has\\qbackslash.ts" },
      { name: 'has"quote.ts' },
    ] as unknown as FileDiffMetadata[];
    expect([...indexDiffFilesByPath(files).keys()]).toEqual(["has\\qbackslash.ts", 'has"quote.ts']);
  });
});

describe("indexDiffKindsByPath", () => {
  /* 種別は patch の header からしか分からない。追加/削除を +N -M から推測すると、
   * 空ファイルの追加(+0 -0)も全行書き換え(+N -N)も誤判定する。 */
  it("header から新規追加・削除・変更を振り分ける", () => {
    const patch = [
      "diff --git a/src/new.ts b/src/new.ts",
      "new file mode 100644",
      "index 0000000..ce01362",
      "--- /dev/null",
      "+++ b/src/new.ts",
      "@@ -0,0 +1 @@",
      "+hello",
      "diff --git a/src/gone.ts b/src/gone.ts",
      "deleted file mode 100644",
      "index ce01362..0000000",
      "--- a/src/gone.ts",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-bye",
      "diff --git a/src/edit.ts b/src/edit.ts",
      "index 1111111..2222222 100644",
      "--- a/src/edit.ts",
      "+++ b/src/edit.ts",
      "@@ -1 +1 @@",
      "-old",
      "+new",
      "",
    ].join("\n");
    expect(indexDiffKindsByPath(parseDiffFiles(patch))).toEqual(
      new Map([
        ["src/new.ts", "added"],
        ["src/gone.ts", "deleted"],
        ["src/edit.ts", "modified"],
      ]),
    );
  });

  /* 内容が変わったかは行の +N -M が示すので、一覧では 2 種に割らない。 */
  it("rename は内容変更の有無によらず移動に畳む", () => {
    const rename = (from: string, to: string, similarity: string) =>
      [
        `diff --git a/${from} b/${to}`,
        `similarity index ${similarity}`,
        `rename from ${from}`,
        `rename to ${to}`,
        "",
      ].join("\n");
    const patch =
      rename("src/pure.ts", "src/moved.ts", "100%") +
      rename("src/edited.ts", "app/edited.ts", "87%") +
      ["--- a/src/edited.ts", "+++ b/app/edited.ts", "@@ -1 +1 @@", "-old", "+new", ""].join("\n");
    expect(indexDiffKindsByPath(parseDiffFiles(patch))).toEqual(
      new Map([
        ["src/moved.ts", "renamed"],
        ["app/edited.ts", "renamed"],
      ]),
    );
  });

  /* file type change(regular file <-> symlink)はサーバーが base 側の削除と
   * final 側の追加を連結して出す。先頭の block を採ると「削除された」と読める。 */
  it("同じ path の削除 + 追加は置換として変更に畳む", () => {
    const patch = [
      "diff --git a/swap b/swap",
      "deleted file mode 100644",
      "index 1111111..0000000",
      "--- a/swap",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-content",
      "diff --git a/swap b/swap",
      "new file mode 120000",
      "index 0000000..3333333",
      "--- /dev/null",
      "+++ b/swap",
      "@@ -0,0 +1 @@",
      "+target",
      "",
    ].join("\n");
    expect(indexDiffKindsByPath(parseDiffFiles(patch))).toEqual(new Map([["swap", "modified"]]));
  });

  /* 不正 UTF-8 の 1 byte は U+FFFD 1 個へ潰れるので、別 file が同じ key になる。
   * 衝突も「削除 + 追加」の形をしていて種別だけでは置換と区別できない。畳むと
   * 実在する追加と削除の両方が「変更」に化けるため、先に見た側を残す。 */
  it("正規化後だけ一致する別 path の削除 + 追加は畳まない", () => {
    const block = (name: string, header: string) =>
      [`diff --git "a/${name}" "b/${name}"`, header, ""].join("\n");
    const patch =
      block("docs/\\200.md", "new file mode 100644") +
      block("docs/\\201.md", "deleted file mode 100644");
    const kinds = indexDiffKindsByPath(parseDiffFiles(patch));
    expect([...kinds.keys()]).toEqual(["docs/�.md"]);
    expect(kinds.get("docs/�.md")).toBe("added");
  });

  /* key は indexDiffFilesByPath と同じ正規化を通さないと、非 ASCII の file だけ
   * サイドバーでアイコンが落ちる(サーバーの files[].path は生のまま)。 */
  it("非 ASCII の quoted path を生のパスで引ける", () => {
    const quoted = '"a/docs/\\346\\227\\245\\346\\234\\254\\350\\252\\236.md"';
    const patch = [
      `diff --git ${quoted} ${quoted.replace("a/", "b/")}`,
      "new file mode 100644",
      "--- /dev/null",
      `+++ ${quoted.replace("a/", "b/")}`,
      "@@ -0,0 +1 @@",
      "+new",
      "",
    ].join("\n");
    expect(indexDiffKindsByPath(parseDiffFiles(patch))).toEqual(
      new Map([["docs/日本語.md", "added"]]),
    );
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
    expect(planDiffFiles([f!])[0]!.initiallyCollapsed).toBe(false);
  });
});

describe("diffMeta", () => {
  it("merge-base 短縮 SHA・取得時刻・file 数・合計統計を並べる", () => {
    expect(diffMeta(makeDiffResponse())).toMatch(
      /^merge-base 0123456789 · captured \d{2}:\d{2}:\d{2} · 1 files \+1\/-1$/,
    );
  });
});
