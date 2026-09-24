import { describe, expect, it } from "vitest";
import { makePane, makePr, makeSession, makeSnapshot } from "../../test/fixtures";
import type { PRRef, PRStack } from "../../transport/types";
import { buildStackIndex, groupPrs, stackOf, type StackView } from "./stack";

/* head / base だけを変えた同 repository の OPEN PR。 */
const chainPr = (number: number, head: string, base: string, over: Partial<PRRef> = {}): PRRef =>
  makePr({ number, headRef: head, baseRef: base, ...over });

/* PR ごとに 1 行ずつ持つ snapshot の索引。 */
const indexOf = (...prs: PRRef[]) =>
  buildStackIndex(
    makeSnapshot([
      makeSession(
        "#1",
        prs.map((pr, i) => makePane({ issueNum: 200 + i, slug: `row-${pr.number}`, prs: [pr] })),
      ),
    ]),
  );

/* 比較しやすい形に畳む: 種別・位置・総数・trunk・層の番号(下から)。 */
const summary = (view: StackView | null) =>
  view && {
    kind: view.kind,
    position: view.position,
    size: view.size,
    baseRef: view.baseRef,
    layers: view.layers.map((l) => l.pr.number),
  };

const slim = (number: number, head: string): PRRef => ({
  number,
  state: "OPEN",
  mergedAt: null,
  headRef: head,
});

const native12 = (position: number, entries?: PRStack["entries"]): PRStack => ({
  number: 12,
  size: 3,
  baseRef: "main",
  position,
  entries,
});

const entries12 = [
  { position: 1, pr: slim(843, "s/a") },
  { position: 2, pr: slim(844, "s/b") },
  { position: 3, pr: slim(845, "s/c") },
];

describe("stackOf", () => {
  const a = chainPr(1, "a", "main");
  const b = chainPr(2, "b", "a");
  const cases: { name: string; prs: PRRef[]; of: number; want: ReturnType<typeof summary> }[] = [
    {
      name: "native stack は entries の全層を position 昇順で持つ",
      prs: [
        chainPr(844, "s/b", "s/a", {
          stack: native12(
            2,
            [...entries12].sort((x, y) => y.position - x.position),
          ),
        }),
      ],
      of: 844,
      want: { kind: "native", position: 2, size: 3, baseRef: "main", layers: [843, 844, 845] },
    },
    {
      name: "entries が無い native stack は自分の層だけを描き、総数は stack から",
      prs: [chainPr(844, "s/b", "s/a", { stack: native12(2) })],
      of: 844,
      want: { kind: "native", position: 2, size: 3, baseRef: "main", layers: [844] },
    },
    {
      name: "推定: base を head に持つ PR を下へたどる",
      prs: [a, b],
      of: 2,
      want: { kind: "inferred", position: 2, size: 2, baseRef: "main", layers: [1, 2] },
    },
    {
      name: "推定: 自分の head に積まれた 1 本を上へたどる",
      prs: [a, b],
      of: 1,
      want: { kind: "inferred", position: 1, size: 2, baseRef: "main", layers: [1, 2] },
    },
    {
      name: "推定: 上に 2 本積まれていたら上へは進まない",
      prs: [a, b, chainPr(3, "c", "a")],
      of: 1,
      want: null,
    },
    {
      name: "推定: 分岐の枝からは下へたどれる",
      prs: [a, b, chainPr(3, "c", "a")],
      of: 2,
      want: { kind: "inferred", position: 2, size: 2, baseRef: "main", layers: [1, 2] },
    },
    {
      name: "推定: 循環しても止まる",
      prs: [chainPr(1, "a", "b"), chainPr(2, "b", "a")],
      of: 1,
      want: { kind: "inferred", position: 2, size: 2, baseRef: "a", layers: [2, 1] },
    },
    {
      name: "推定: fork の head は同名 branch とつながない",
      prs: [a, chainPr(2, "b", "a", { headRepo: "someone/fanout" })],
      of: 1,
      want: null,
    },
    {
      name: "推定: 閉じただけの PR はつながない",
      prs: [a, chainPr(2, "b", "a", { state: "CLOSED" })],
      of: 1,
      want: null,
    },
    {
      name: "推定: マージ済みの下層はつなぐ",
      prs: [chainPr(1, "a", "main", { state: "MERGED", mergedAt: "2026-09-01T00:00:00Z" }), b],
      of: 2,
      want: { kind: "inferred", position: 2, size: 2, baseRef: "main", layers: [1, 2] },
    },
    {
      name: "推定: 同じ head なら OPEN の PR を層に採る",
      prs: [
        chainPr(1, "a", "main", { state: "MERGED", mergedAt: "2026-09-01T00:00:00Z" }),
        chainPr(3, "a", "main"),
        b,
      ],
      of: 2,
      want: { kind: "inferred", position: 2, size: 2, baseRef: "main", layers: [3, 2] },
    },
    {
      name: "native stack の PR は推定の連鎖に混ぜない",
      prs: [a, chainPr(2, "b", "a", { stack: native12(2) })],
      of: 1,
      want: null,
    },
    {
      name: "別 repository の同番号 PR は stack を持たない",
      prs: [a, chainPr(2, "b", "a", { baseRepo: "other/repo", headRepo: "other/repo" })],
      of: 2,
      want: null,
    },
  ];

  for (const tt of cases) {
    it(tt.name, () => {
      const index = indexOf(...tt.prs);
      const pr = tt.prs.find((p) => p.number === tt.of);
      expect(pr).toBeDefined();
      expect(summary(stackOf(pr as PRRef, index))).toEqual(tt.want);
    });
  }

  it("他の行が持つ層は、その行のコピー(信号つき)と行を引く", () => {
    const own = chainPr(844, "s/b", "s/a", { stack: native12(2, entries12) });
    const other = chainPr(845, "s/c", "s/b", { stack: native12(3, entries12), ci: "pass" });
    const index = indexOf(own, other);

    const view = stackOf(own, index);
    const top = view?.layers[2];
    expect(top?.pr.ci).toBe("pass");
    expect(top?.owners.map((o) => o.slug)).toEqual(["row-845"]);
    // どの行にも無い層は stack 取得の軽量コピーのまま
    expect(view?.layers[0]?.owners).toEqual([]);
  });
});

describe("groupPrs", () => {
  it("同じ stack の PR は 1 ブロックにまとめ、stack 外は wire 順の 1 行ずつ", () => {
    const layer1 = chainPr(843, "s/a", "main", { stack: native12(1, entries12) });
    const layer2 = chainPr(844, "s/b", "s/a", { stack: native12(2, entries12) });
    const loose = chainPr(9, "x", "main");
    const index = indexOf(layer1, layer2, loose);

    const groups = groupPrs([layer1, loose, layer2], index);
    expect(groups.map((g) => g.key)).toEqual(["native:12", "pr:octo/fanout#9"]);
  });

  it("別 repository の同番号 PR は別の行のまま", () => {
    const here = chainPr(5, "x", "main");
    const there = chainPr(5, "x", "main", { baseRepo: "other/repo", headRepo: "other/repo" });
    const groups = groupPrs([here, there], indexOf(here));
    expect(groups.map((g) => g.key)).toEqual(["pr:octo/fanout#5", "pr:other/repo#5"]);
  });
});
