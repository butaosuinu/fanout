import { describe, expect, it } from "vitest";
import { alignedScrollTop, hiddenAfterViewing, nextVisibleFileIndex } from "./scrollAlign";

/* 確認済みを付けると読んでいた file が畳まれ(または消え)、文書の高さが縮む。
 * 次に読む file の上端を本文の上端へ送るのがここの役目で、jsdom では実測できない
 * ぶん、式と選び方だけを固定する。 */
describe("alignedScrollTop", () => {
  const cases = [
    {
      name: "対象が下にあれば下へスクロールする",
      in: { scrollTop: 100, targetTop: 480, scrollerTop: 80 },
      want: 500,
    },
    {
      name: "対象が上にあれば上へ戻る",
      in: { scrollTop: 900, targetTop: 20, scrollerTop: 80 },
      want: 840,
    },
    {
      name: "すでに上端に揃っていれば動かさない",
      in: { scrollTop: 240, targetTop: 80, scrollerTop: 80 },
      want: 240,
    },
  ];
  for (const tt of cases) {
    it(tt.name, () => {
      expect(alignedScrollTop(tt.in)).toBe(tt.want);
    });
  }
});

/* 「確認済みを隠す」が on のときは、いま押した file がこのあと本文から降りる。
 * setViewed の結果が hidden に出るのは次の render なので、先取りしないと
 * 消える file 自身を送り先に選んでしまう。 */
describe("hiddenAfterViewing", () => {
  it("隠す設定なら押した path の index を足す", () => {
    const got = hiddenAfterViewing({
      hidden: new Set([0]),
      hideViewed: true,
      group: [2],
    });
    expect([...got].sort((a, b) => a - b)).toEqual([0, 2]);
  });

  it("file type change の 2 entry はまとめて降りる", () => {
    const got = hiddenAfterViewing({ hidden: new Set(), hideViewed: true, group: [3, 4] });
    expect([...got].sort((a, b) => a - b)).toEqual([3, 4]);
  });

  it("隠す設定が off なら畳まれるだけで本文には残る", () => {
    const hidden = new Set([1]);
    expect(hiddenAfterViewing({ hidden, hideViewed: false, group: [2] })).toBe(hidden);
  });
});

describe("nextVisibleFileIndex", () => {
  const cases = [
    {
      name: "隠れていなければ次の index",
      in: { from: 0, count: 3, hidden: new Set<number>() },
      want: 1,
    },
    {
      name: "隠れている file は飛ばす",
      in: { from: 0, count: 4, hidden: new Set([1, 2]) },
      want: 3,
    },
    {
      name: "最後の file なら送り先が無い",
      in: { from: 2, count: 3, hidden: new Set<number>() },
      want: null,
    },
    {
      name: "後続が全部隠れていれば送り先が無い",
      in: { from: 0, count: 3, hidden: new Set([1, 2]) },
      want: null,
    },
    {
      // 手前の file が隠れていても、送るのは後ろ側だけ
      name: "手前の隠れた file へは戻らない",
      in: { from: 2, count: 4, hidden: new Set([0, 1]) },
      want: 3,
    },
  ];
  for (const tt of cases) {
    it(tt.name, () => {
      expect(nextVisibleFileIndex(tt.in)).toBe(tt.want);
    });
  }
});
