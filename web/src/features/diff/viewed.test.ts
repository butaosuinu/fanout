import { beforeEach, describe, expect, it } from "vitest";
import { parseDiffFiles } from "./diff";
import { diffFilePaths, fileFingerprint, indexFingerprintsByPath, viewedIndices } from "./viewed";
import { loadViewed, saveViewed } from "./viewedStore";

/* 実パーサを通す — fingerprint は FileDiffMetadata の形に依存するので、手で組んだ
 * オブジェクトで固定するとライブラリ更新に対して何も守れない(diff.test.ts と同じ方針)。 */
function patchOf(path: string, from: string, to: string): string {
  return [
    `diff --git a/${path} b/${path}`,
    `--- a/${path}`,
    `+++ b/${path}`,
    "@@ -1 +1 @@",
    `-${from}`,
    `+${to}`,
    "",
  ].join("\n");
}

function fingerprintOf(patch: string): string {
  const files = parseDiffFiles(patch);
  return fileFingerprint(files[0]!);
}

describe("fileFingerprint", () => {
  it("同じ patch からは同じ値を返す", () => {
    const patch = patchOf("a.ts", "old", "new");
    expect(fingerprintOf(patch)).toBe(fingerprintOf(patch));
  });

  it("追加行の中身が変われば別の値になる", () => {
    const before = fingerprintOf(patchOf("a.ts", "old", "new"));
    const after = fingerprintOf(patchOf("a.ts", "old", "newer"));
    expect(after).not.toBe(before);
  });

  it("行の切れ目の違いを取り違えない", () => {
    /* 連結すると同じ文字列になる 2 通りの行分割。長さを畳み込んでいないと衝突する */
    const split = fingerprintOf(
      ["diff --git a/a.ts b/a.ts", "--- a/a.ts", "+++ b/a.ts", "@@ -1 +2 @@", "+ab", "+c", ""].join(
        "\n",
      ),
    );
    const joined = fingerprintOf(
      ["diff --git a/a.ts b/a.ts", "--- a/a.ts", "+++ b/a.ts", "@@ -1 +2 @@", "+a", "+bc", ""].join(
        "\n",
      ),
    );
    expect(joined).not.toBe(split);
  });

  it("index 行を持たない pure rename でも値を返す", () => {
    /* rename だけの patch は `index` 行が無いので newObjectId が undefined になる */
    const patch = [
      "diff --git a/old.ts b/new.ts",
      "similarity index 100%",
      "rename from old.ts",
      "rename to new.ts",
      "",
    ].join("\n");
    expect(fingerprintOf(patch)).toMatch(/^[0-9a-z]+$/);
  });
});

describe("indexFingerprintsByPath", () => {
  it("path ごとに 1 エントリへ束ねる", () => {
    const files = parseDiffFiles(patchOf("a.ts", "1", "2") + patchOf("b.ts", "3", "4"));
    expect([...indexFingerprintsByPath(files).keys()]).toEqual(["a.ts", "b.ts"]);
  });

  it("file type change の同 path 2 entry を 1 つに畳む", () => {
    /* 削除 + 追加の 2 block で 1 file を表す形。確認済みも 1 つとして扱う */
    const patch = [
      "diff --git a/a.ts b/a.ts",
      "deleted file mode 100644",
      "--- a/a.ts",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-old",
      "diff --git a/a.ts b/a.ts",
      "new file mode 120000",
      "--- /dev/null",
      "+++ b/a.ts",
      "@@ -0,0 +1 @@",
      "+target",
      "",
    ].join("\n");
    const fingerprints = indexFingerprintsByPath(parseDiffFiles(patch));
    expect([...fingerprints.keys()]).toEqual(["a.ts"]);
    /* 両方を畳んだ値なので、片方だけの値とは一致しない */
    expect(fingerprints.get("a.ts")).toContain(".");
  });
});

describe("diffFilePaths / viewedIndices", () => {
  it("patch 中の path を index 順に返す", () => {
    const files = parseDiffFiles(patchOf("src/a.ts", "1", "2") + patchOf("b.ts", "3", "4"));
    expect(diffFilePaths(files)).toEqual(["src/a.ts", "b.ts"]);
  });

  it("確認済みの path を plan index の集合へ写す", () => {
    const paths = ["a.ts", "b.ts", "c.ts"];
    expect([...viewedIndices(paths, new Set(["a.ts", "c.ts"]))]).toEqual([0, 2]);
  });

  it("patch に無い path は index を持たないので無視する", () => {
    expect(viewedIndices(["a.ts"], new Set(["gone.ts"])).size).toBe(0);
  });
});

describe("viewedStore", () => {
  beforeEach(() => localStorage.clear());

  it("保存した path と fingerprint を読み戻す", () => {
    saveViewed("142#101", new Map([["a.ts", "fp1"]]));
    expect([...loadViewed("142#101")]).toEqual([["a.ts", "fp1"]]);
  });

  it("scope が違えば混ざらない", () => {
    saveViewed("142#101", new Map([["a.ts", "fp1"]]));
    expect(loadViewed("142#102").size).toBe(0);
  });

  it("空になったら scope ごとキーを消す", () => {
    saveViewed("142#101", new Map([["a.ts", "fp1"]]));
    saveViewed("142#101", new Map());
    expect(localStorage.getItem("fanout.diffViewed.142#101")).toBeNull();
  });

  it("JSON ですらない保存値を落とす", () => {
    localStorage.setItem("fanout.diffViewed.142#101", "not json");
    expect(loadViewed("142#101").size).toBe(0);
  });

  it("知らない版の保存値を落とす", () => {
    const raw = JSON.stringify({ v: 2, t: 1, files: { "a.ts": "fp1" } });
    localStorage.setItem("fanout.diffViewed.142#101", raw);
    expect(loadViewed("142#101").size).toBe(0);
  });

  it("files が配列や null の保存値を落とす", () => {
    for (const files of [[], null, "a.ts"]) {
      localStorage.setItem("fanout.diffViewed.142#101", JSON.stringify({ v: 1, t: 1, files }));
      expect(loadViewed("142#101").size).toBe(0);
    }
  });

  it("string でない fingerprint の行だけを捨てる", () => {
    const raw = JSON.stringify({ v: 1, t: 1, files: { "a.ts": "fp1", "b.ts": 7, "c.ts": null } });
    localStorage.setItem("fanout.diffViewed.142#101", raw);
    expect([...loadViewed("142#101")]).toEqual([["a.ts", "fp1"]]);
  });

  it("contract 上限の 500 件で読み込みを打ち切る", () => {
    const files: Record<string, string> = {};
    for (let i = 0; i < 600; i++) files[`f${i}.ts`] = "fp";
    localStorage.setItem("fanout.diffViewed.142#101", JSON.stringify({ v: 1, t: 1, files }));
    expect(loadViewed("142#101").size).toBe(500);
  });

  it("scope が上限を超えたら最終更新の古いほうから捨てる", () => {
    /* 上限 8 本。0..8 の 9 scope を古い順に書き、9 本目で最古が落ちる */
    for (let i = 0; i <= 8; i++) {
      localStorage.setItem(
        `fanout.diffViewed.old${i}`,
        JSON.stringify({ v: 1, t: i, files: { "a.ts": "fp" } }),
      );
    }
    saveViewed("new", new Map([["a.ts", "fp"]]));
    expect(loadViewed("old0").size).toBe(0);
    expect(loadViewed("old8").size).toBe(1);
    expect(loadViewed("new").size).toBe(1);
  });
});
