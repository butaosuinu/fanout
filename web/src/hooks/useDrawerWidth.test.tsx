import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { useDrawerWidth } from "./useDrawerWidth";

/* hook 単体のテスト。jsdom には pointer capture が無いので grip 要素へ直接
 * pointer イベントを発火する(実ブラウザでは setPointerCapture が grip 外の
 * move/up も同じ要素へ配送することを保証する)。 */

function Probe() {
  const { width, gripProps } = useDrawerWidth();
  return (
    <div>
      <div data-testid="grip" {...gripProps} />
      <output data-testid="width">{width}</output>
    </div>
  );
}

const width = () => Number(screen.getByTestId("width").textContent);
const grip = () => screen.getByTestId("grip");

/* hook はビューポート上限(innerWidth - 360 / ≤1100px は 90vw)でも clamp する。
 * jsdom の既定 innerWidth(1024)だと上限 921px になり値が読みにくいので、
 * 既定は広い画面に固定し、ビューポート clamp 自体は専用ケースで検証する。 */
const setInnerWidth = (w: number) =>
  Object.defineProperty(window, "innerWidth", { value: w, configurable: true, writable: true });

beforeEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("drawer-resizing");
  setInnerWidth(2000); // 上限 = min(1416, 2000 - 360) = 1416(コンテンツ幅 cap)
});

describe("useDrawerWidth", () => {
  it("保存値が無ければデフォルト 840px", () => {
    render(<Probe />);
    expect(width()).toBe(840);
  });

  it("localStorage の保存値から同期初期化する", () => {
    localStorage.setItem("fanout.drawerWidth", "560");
    render(<Probe />);
    expect(width()).toBe(560);
  });

  it("保存値は 320–1416px に clamp し、不正値はデフォルトへ落とす", () => {
    localStorage.setItem("fanout.drawerWidth", "100");
    const small = render(<Probe />);
    expect(width()).toBe(320);
    small.unmount();

    localStorage.setItem("fanout.drawerWidth", "99999");
    const large = render(<Probe />);
    expect(width()).toBe(1416);
    large.unmount();

    localStorage.setItem("fanout.drawerWidth", "abc");
    render(<Probe />);
    expect(width()).toBe(840);
  });

  it("左ドラッグで拡大・右ドラッグで縮小し、pointerup でのみ永続化する", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    expect(document.documentElement).toHaveClass("drawer-resizing");

    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 700, buttons: 1 });
    expect(width()).toBe(940); // 840 + (800 - 700)
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull(); // ドラッグ中は未保存

    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 860, buttons: 1 });
    expect(width()).toBe(780); // 840 + (800 - 860)

    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 860 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("780");
  });

  it("ドラッグ中の幅も 320–1416px に clamp する", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: -2000, buttons: 1 });
    expect(width()).toBe(1416);
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 3000, buttons: 1 });
    expect(width()).toBe(320);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 3000 });
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("320");
  });

  it("ドラッグ外の pointermove・別 pointerId の move は無視する", () => {
    render(<Probe />);
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 100, buttons: 1 });
    expect(width()).toBe(840);

    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 2, clientX: 100, buttons: 1 });
    expect(width()).toBe(840);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 800 });
  });

  it("pointercancel はドラッグを終了するが永続化しない", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 700, buttons: 1 });
    fireEvent.pointerCancel(grip(), { pointerId: 1 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull();
  });

  it("ドラッグ中の unmount で html.drawer-resizing を残さない", () => {
    const probe = render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    expect(document.documentElement).toHaveClass("drawer-resizing");
    probe.unmount();
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
  });

  it("ダブルクリックでデフォルトに戻し、保存値を消す", () => {
    localStorage.setItem("fanout.drawerWidth", "560");
    render(<Probe />);
    expect(width()).toBe(560);
    fireEvent.doubleClick(grip());
    expect(width()).toBe(840);
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull();
  });

  it("ArrowLeft で +24px / ArrowRight で -24px、即永続化する", () => {
    render(<Probe />);
    fireEvent.keyDown(grip(), { key: "ArrowLeft" });
    expect(width()).toBe(864);
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("864");

    fireEvent.keyDown(grip(), { key: "ArrowRight" });
    fireEvent.keyDown(grip(), { key: "ArrowRight" });
    expect(width()).toBe(816);
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("816");
  });

  it("修飾キー付きの矢印(Alt+← = 履歴等)は奪わない", () => {
    render(<Probe />);
    fireEvent.keyDown(grip(), { key: "ArrowLeft", altKey: true });
    fireEvent.keyDown(grip(), { key: "ArrowLeft", metaKey: true });
    fireEvent.keyDown(grip(), { key: "ArrowLeft", ctrlKey: true });
    expect(width()).toBe(840);
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull();
  });

  it("狭い画面では描画を viewport 上限に抑え、intent(保存値)は静的上限まで伸ばす", () => {
    setInnerWidth(1440); // viewport 上限 1080、静的上限 1416
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: -2000, buttons: 1 });
    expect(width()).toBe(1080); // 描画はビューポート上限で頭打ち
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: -2000 });
    // intent は静的上限まで伸びる(広い画面に移れば 1416 で描画される)
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("1416");
  });

  it("≤1100px のオーバーレイでは上限 90vw、保存値の読込も同様に clamp する", () => {
    setInnerWidth(1000); // 上限 900
    localStorage.setItem("fanout.drawerWidth", "1600");
    render(<Probe />);
    expect(width()).toBe(900);
  });

  it("ビューポート縮小では描画だけ追従し、次ドラッグは見えている幅起点(デッドゾーンなし)", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: -2000, buttons: 1 });
    expect(width()).toBe(1416); // 2000px の上限(コンテンツ幅 cap)
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: -2000 });

    // ウィンドウを 1200px に縮小 → 描画は min(1416, 1200-360)=840 へ追従
    setInnerWidth(1200);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(width()).toBe(840);

    // 次のドラッグは見えている 840 から始まる(stale な 1416 起点にならない)
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 2, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 2, clientX: 820, buttons: 1 }); // 右 20px = 縮小
    expect(width()).toBe(820);
    fireEvent.pointerUp(grip(), { pointerId: 2, clientX: 820 });
  });

  it("描画上限に張り付いた状態の拡大ドラッグは大きい intent(保存値)を縮めない", () => {
    localStorage.setItem("fanout.drawerWidth", "1300");
    setInnerWidth(1200); // viewport 上限 840
    render(<Probe />);
    expect(width()).toBe(840); // 1300 は cap されて 840 描画

    // 拡大方向(左 20px)に引いても描画は 840 のまま、intent 1300 は維持
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 780, buttons: 1 });
    expect(width()).toBe(840);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 780 });

    // 広い画面に戻すと設定した 1300 が復元する(860 に縮んでいない)
    setInnerWidth(2000);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(width()).toBe(1300);
  });

  it("描画上限に張り付いた状態でも縮小ドラッグは見えている幅から追従する", () => {
    localStorage.setItem("fanout.drawerWidth", "1300");
    setInnerWidth(1200); // viewport 上限 840
    render(<Probe />);
    expect(width()).toBe(840);

    // 縮小方向(右 40px)→ 見えている 840 から 800 へ
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 840, buttons: 1 });
    expect(width()).toBe(800);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 840 });
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("800");
  });

  it("bottom sheet 相当の縮小を経ても intent を失わず、広げれば設定幅へ復元する", () => {
    localStorage.setItem("fanout.drawerWidth", "1300");
    render(<Probe />);
    expect(width()).toBe(1300); // 2000px: min(1300, 1416)

    // 800px(bottom sheet 帯)へ縮小 → 描画は 720(800*0.9)だが intent は保持
    setInnerWidth(800);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(width()).toBe(720);

    // 2000px に戻すと設定した 1300 に復元(下方向 clamp で失わない)
    setInnerWidth(2000);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(width()).toBe(1300);
  });

  it("無移動クリックでは永続化しない(保存値なし = デフォルト追従を保つ)", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 800 });
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull();
  });

  it("微調整ドラッグ直後の dblclick はリセットとして扱わない", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 760, buttons: 1 }); // 40px 移動
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 760 });
    expect(width()).toBe(880);
    fireEvent.doubleClick(grip()); // 直前ドラッグの誤爆 → 無視
    expect(width()).toBe(880);
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("880");
  });

  it("ボタン解放を取り逃しても buttons=0 の move でドラッグを終了する", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800, isPrimary: true });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 700, buttons: 1 });
    expect(width()).toBe(940);
    // capture が効かない環境: pointerup が grip 外で起き、buttons=0 の move だけ届く
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 600, buttons: 0 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(width()).toBe(940); // 取り逃し後の hover では動かない
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("940");
  });
});
