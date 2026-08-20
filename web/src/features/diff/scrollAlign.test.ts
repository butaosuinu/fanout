import { describe, expect, it } from "vitest";
import { alignedScrollTop, nextFileToRead } from "./scrollAlign";

/* 確認済みを付けると読んでいた file が畳まれ(または消え)、文書の高さが縮む。
 * 次に読む file の上端を本文の上端へ送るのがここの役目で、jsdom では実測できない
 * ぶん、式と選び方だけを固定する。 */
describe("alignedScrollTop", () => {
  const tests = [
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
  for (const tt of tests) {
    it(tt.name, () => {
      expect(alignedScrollTop(tt.in)).toBe(tt.want);
    });
  }
});

describe("nextFileToRead", () => {
  const tests = [
    {
      name: "隠れていなければ次の index",
      in: { from: 0, count: 3, group: [0], hidden: new Set<number>() },
      want: 1,
    },
    {
      name: "最後の file なら送り先が無い",
      in: { from: 2, count: 3, group: [2], hidden: new Set<number>() },
      want: null,
    },
    {
      /* 「確認済みを隠す」が on。隠れる集合は 1 render 遅れるので、group を飛ばす
         ことで押した file 自身を送り先に選ばずに済む。 */
      name: "隠れている file は飛ばす",
      in: { from: 0, count: 4, group: [0], hidden: new Set([1, 2]) },
      want: 3,
    },
    {
      name: "後続が全部隠れていれば送り先が無い",
      in: { from: 0, count: 3, group: [0], hidden: new Set([1, 2]) },
      want: null,
    },
    {
      /* file type change は同じ path が 2 entry になる。押した側の隣がその片割れ
         なので、隠す設定が off でも(= 本文に残っていても)そこへは送らない。 */
      name: "同じ path のもう片方は本文に残っていても飛ばす",
      in: { from: 3, count: 6, group: [3, 4], hidden: new Set<number>() },
      want: 5,
    },
    {
      // 手前の file が隠れていても、送るのは後ろ側だけ
      name: "手前の隠れた file へは戻らない",
      in: { from: 2, count: 4, group: [2], hidden: new Set([0, 1]) },
      want: 3,
    },
  ];
  for (const tt of tests) {
    it(tt.name, () => {
      expect(nextFileToRead(tt.in)).toBe(tt.want);
    });
  }
});
