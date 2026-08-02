import { describe, expect, it } from "vitest";
import { coversBackground, panelWidthFor } from "./diffView";

/* 覆っているかはモードだけでは決まらない。パネルは右端がドロワーの左端に貼り付き、
 * そこを越えた分は左へ伸びるので、1,100px を超える画面でも一覧が 1px も残らない
 * 配置がある。覆っているのに非モーダルだと、見えない一覧へ Tab が抜け、隠れた
 * peek のポーリングも止まらない。 */
describe("coversBackground", () => {
  it("全画面は常に覆う", () => {
    expect(
      coversBackground({ view: "full", viewportWidth: 2560, anchorRight: 840, width: 760 }),
    ).toBe(true);
  });

  it("1,100px 以下はコンパクトでも覆う(CSS が全幅パネルにする)", () => {
    expect(
      coversBackground({ view: "compact", viewportWidth: 1100, anchorRight: 0, width: 760 }),
    ).toBe(true);
  });

  it("一覧が残る配置は覆わない", () => {
    // 2,560px・ドロワー 840px・パネル 760px → パネルは x=960-1720、一覧は 0-960 が見える
    expect(
      coversBackground({ view: "compact", viewportWidth: 2560, anchorRight: 840, width: 760 }),
    ).toBe(false);
  });

  it("広いドロワー + 広いパネルで一覧が消える配置は覆う", () => {
    /* 1,200px・ドロワー 840px(= anchorRight)・パネル 760px。右端は
     * min(840, 1200-760=440) = 440 なのでパネルは x=0-760 を占め、一覧の
     * 0-360 は完全に隠れる。1,100px を超えているのでモードだけでは判定できない。 */
    expect(
      coversBackground({ view: "compact", viewportWidth: 1200, anchorRight: 840, width: 760 }),
    ).toBe(true);
  });

  it("ドロワーが無ければ 95% でも 5% 残るので覆わない", () => {
    expect(
      coversBackground({ view: "compact", viewportWidth: 2560, anchorRight: 0, width: 2432 }),
    ).toBe(false);
  });
});

/* covering と混同しない。ドロワーが広くて一覧が隠れる配置でも、パネルの実幅は
 * compactWidth のまま。ビューポート幅で判定すると、狭い本文を左右 2 面に割り、
 * container query でサイドバーまで消える。 */
describe("panelWidthFor", () => {
  it("全画面はビューポート幅", () => {
    expect(panelWidthFor({ view: "full", viewportWidth: 2560, compactWidth: 760 })).toBe(2560);
  });

  it("1,100px 以下のコンパクトはビューポート幅(CSS が全幅パネルにする)", () => {
    expect(panelWidthFor({ view: "compact", viewportWidth: 1100, compactWidth: 760 })).toBe(1100);
  });

  it("覆っていてもコンパクトの実幅を使う", () => {
    const geometry = {
      view: "compact",
      viewportWidth: 1200,
      anchorRight: 840,
      width: 760,
    } as const;
    expect(coversBackground(geometry)).toBe(true); // 一覧は隠れる
    expect(panelWidthFor({ view: "compact", viewportWidth: 1200, compactWidth: 760 })).toBe(760);
  });
});
