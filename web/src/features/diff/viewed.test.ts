import { beforeEach, describe, expect, it } from "vitest";
import { parseDiffFiles } from "./diff";
import { diffFilePaths, fileFingerprint, indexFingerprintsByPath, indicesForPaths } from "./viewed";
import { loadViewed, setViewedPath, viewedScope } from "./viewedStore";

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

  it("mode だけの変更とその取り消しを取り違えない", () => {
    /* chmod は `index` 行も hunk も持たないので、mode を混ぜないと同じ値になる */
    const modeOnly = (from: string, to: string) =>
      fingerprintOf(
        ["diff --git a/run.sh b/run.sh", `old mode ${from}`, `new mode ${to}`, ""].join("\n"),
      );
    expect(modeOnly("100644", "100755")).not.toBe(modeOnly("100755", "100644"));
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

  /* 不正 UTF-8 の byte は正規化で U+FFFD へ潰れるので、別の file が同じ key に
   * 集まる。畳んでしまうと、片方をチェックしただけでもう片方も確認済みになり、
   * 隠す設定なら読んでいない変更が本文からも一覧からも消える。 */
  it("正規化で衝突した別 file の key は fingerprint を持たせない", () => {
    const patch = [
      'diff --git "a/docs/\\200.md" "b/docs/\\200.md"',
      '--- "a/docs/\\200.md"',
      '+++ "b/docs/\\200.md"',
      "@@ -1 +1 @@",
      "-one",
      "+ONE",
      'diff --git "a/docs/\\201.md" "b/docs/\\201.md"',
      '--- "a/docs/\\201.md"',
      '+++ "b/docs/\\201.md"',
      "@@ -1 +1 @@",
      "-two",
      "+TWO",
      "",
    ].join("\n");
    const files = parseDiffFiles(patch);
    const paths = diffFilePaths(files);
    // 前提: 2 つの file が同じ key へ潰れている
    expect(paths[0]).toBe(paths[1]);
    expect(indexFingerprintsByPath(files).size).toBe(0);
  });

  it("U+FFFD を含まない同 path の 2 entry は畳む(file type change)", () => {
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
    expect(indexFingerprintsByPath(parseDiffFiles(patch)).has("a.ts")).toBe(true);
  });
});

describe("diffFilePaths / indicesForPaths", () => {
  it("patch 中の path を index 順に返す", () => {
    const files = parseDiffFiles(patchOf("src/a.ts", "1", "2") + patchOf("b.ts", "3", "4"));
    expect(diffFilePaths(files)).toEqual(["src/a.ts", "b.ts"]);
  });

  it("確認済みの path を plan index の集合へ写す", () => {
    const paths = ["a.ts", "b.ts", "c.ts"];
    expect([...indicesForPaths(paths, new Set(["a.ts", "c.ts"]))]).toEqual([0, 2]);
  });

  it("patch に無い path は index を持たないので無視する", () => {
    expect(indicesForPaths(["a.ts"], new Set(["gone.ts"])).size).toBe(0);
  });

  it("Map も member として引ける(fingerprint の key からチェック可否を出す)", () => {
    const paths = ["a.ts", "b.ts"];
    expect([...indicesForPaths(paths, new Map([["b.ts", "fp"]]))]).toEqual([1]);
  });
});

describe("viewedStore", () => {
  beforeEach(() => localStorage.clear());

  it("保存した path と fingerprint を読み戻す", () => {
    setViewedPath("142#101", "a.ts", "fp1");
    expect([...loadViewed("142#101")]).toEqual([["a.ts", "fp1"]]);
  });

  it("scope が違えば混ざらない", () => {
    setViewedPath("142#101", "a.ts", "fp1");
    expect(loadViewed("142#102").size).toBe(0);
  });

  it("空になったら scope ごとキーを消す", () => {
    setViewedPath("142#101", "a.ts", "fp1");
    setViewedPath("142#101", "a.ts", null);
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

  it("files が配列の保存値を落とす", () => {
    localStorage.setItem("fanout.diffViewed.142#101", '{"v":1,"t":1,"files":[]}');
    expect(loadViewed("142#101").size).toBe(0);
  });

  it("files が null の保存値を落とす", () => {
    localStorage.setItem("fanout.diffViewed.142#101", '{"v":1,"t":1,"files":null}');
    expect(loadViewed("142#101").size).toBe(0);
  });

  it("files が文字列の保存値を落とす", () => {
    localStorage.setItem("fanout.diffViewed.142#101", '{"v":1,"t":1,"files":"a.ts"}');
    expect(loadViewed("142#101").size).toBe(0);
  });

  it("string でない fingerprint の行だけを捨てる", () => {
    const raw = JSON.stringify({ v: 1, t: 1, files: { "a.ts": "fp1", "b.ts": 7, "c.ts": null } });
    localStorage.setItem("fanout.diffViewed.142#101", raw);
    expect([...loadViewed("142#101")]).toEqual([["a.ts", "fp1"]]);
  });

  it("有限でない t の保存値を落とす", () => {
    /* JSON.parse は 1e999 を Infinity にするので typeof === "number" は通る。
       剪定の比較関数が NaN を返し、並び順が実装依存になるのを防ぐ。 */
    localStorage.setItem("fanout.diffViewed.142#101", '{"v":1,"t":1e999,"files":{"a.ts":"fp1"}}');
    expect(loadViewed("142#101").size).toBe(0);
  });

  /* 上限に当たったときに捨てるのは古いほうで、いま入れたチェックは残ること。
     逆にすると「上限に達した瞬間から保存が黙って効かなくなる」。 */
  it("contract 上限の 500 件を超えたら古いほうから捨てる", () => {
    for (let i = 0; i < 600; i++) setViewedPath("142#101", `f${i}.ts`, "fp");
    const loaded = loadViewed("142#101");
    expect(loaded.size).toBe(500);
    expect(loaded.has("f599.ts")).toBe(true);
    expect(loaded.has("f0.ts")).toBe(false);
  });

  it("scope が上限を超えたら最終更新の古いほうから捨てる", () => {
    /* 上限 50 本。0..50 の 51 scope を古い順に書き、次の書き込みで最古が落ちる */
    for (let i = 0; i <= 50; i++) {
      localStorage.setItem(
        `fanout.diffViewed.old${i}`,
        JSON.stringify({ v: 1, t: i, files: { "a.ts": "fp" } }),
      );
    }
    setViewedPath("new", "a.ts", "fp");
    expect(loadViewed("old0").size).toBe(0);
    expect(loadViewed("old50").size).toBe(1);
    expect(loadViewed("new").size).toBe(1);
  });

  it("上限ちょうどの scope 数では何も捨てない", () => {
    for (let i = 0; i < 49; i++) {
      localStorage.setItem(
        `fanout.diffViewed.old${i}`,
        JSON.stringify({ v: 1, t: i, files: { "a.ts": "fp" } }),
      );
    }
    setViewedPath("new", "a.ts", "fp");
    expect(loadViewed("old0").size).toBe(1);
  });

  /* 書き戻しは 1 file 分。別タブが直前に書いた分を巻き戻さないこと。 */
  it("書き込み前に読み直すので、間に入った別の書き込みを消さない", () => {
    setViewedPath("142#101", "a.ts", "fp1");
    // 別タブが書いた体(こちらの state には無い)
    const raw = JSON.parse(localStorage.getItem("fanout.diffViewed.142#101")!);
    raw.files["b.ts"] = "fp2";
    localStorage.setItem("fanout.diffViewed.142#101", JSON.stringify(raw));

    setViewedPath("142#101", "c.ts", "fp3");

    expect([...loadViewed("142#101").keys()].sort()).toEqual(["a.ts", "b.ts", "c.ts"]);
  });

  /* 上限で落とすのは古い順なので、入れ直した path は末尾へ動かす。
     Map.set は既存 key の位置を変えないため、明示的に delete してから入れる。 */
  it("既存 path を入れ直すと、いちばん新しい扱いになる", () => {
    setViewedPath("142#101", "a.ts", "fp1");
    setViewedPath("142#101", "b.ts", "fp2");
    setViewedPath("142#101", "a.ts", "fp1-new");
    expect([...loadViewed("142#101").keys()]).toEqual(["b.ts", "a.ts"]);
  });
});

describe("viewedScope", () => {
  /* localStorage は origin ごとなので、ポートを固定して別リポジトリを開くと
     parent#issue 形式の rowKey がそのまま衝突する。 */
  it("repo が違えば別の scope になる", () => {
    expect(viewedScope("owner/a", "142#101")).not.toBe(viewedScope("owner/b", "142#101"));
  });

  it("repo と rowKey の境界が動いても衝突しない", () => {
    /* エンコードしないと "a/b" + "c" と "a" + "b/c" が同じ文字列になる */
    expect(viewedScope("a/b", "c")).not.toBe(viewedScope("a", "b/c"));
  });

  it("repo 未解決(空)でも rowKey だけで一意に決まる", () => {
    expect(viewedScope("", "142#101")).not.toBe(viewedScope("", "142#102"));
  });
});
