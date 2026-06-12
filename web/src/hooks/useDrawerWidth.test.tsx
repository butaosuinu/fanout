import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { useDrawerWidth } from "./useDrawerWidth";

/* hook 単体のテスト。jsdom には pointer capture が無いので grip 要素へ直接
 * pointer イベントを発火する(実ブラウザでは setPointerCapture が grip 外の
 * move/up も同じ要素へ配送することを保証する)。 */

function Probe() {
  const { width, gripProps } = useDrawerWidth();
  return (
    <div>
      <div data-testid="grip" tabIndex={0} {...gripProps} />
      <output data-testid="width">{width}</output>
    </div>
  );
}

const width = () => Number(screen.getByTestId("width").textContent);
const grip = () => screen.getByTestId("grip");

beforeEach(() => {
  localStorage.clear();
  document.documentElement.classList.remove("drawer-resizing");
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

  it("保存値は 320–1600px に clamp し、不正値はデフォルトへ落とす", () => {
    localStorage.setItem("fanout.drawerWidth", "100");
    const small = render(<Probe />);
    expect(width()).toBe(320);
    small.unmount();

    localStorage.setItem("fanout.drawerWidth", "99999");
    const large = render(<Probe />);
    expect(width()).toBe(1600);
    large.unmount();

    localStorage.setItem("fanout.drawerWidth", "abc");
    render(<Probe />);
    expect(width()).toBe(840);
  });

  it("左ドラッグで拡大・右ドラッグで縮小し、pointerup でのみ永続化する", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800 });
    expect(document.documentElement).toHaveClass("drawer-resizing");

    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 700 });
    expect(width()).toBe(940); // 840 + (800 - 700)
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull(); // ドラッグ中は未保存

    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 860 });
    expect(width()).toBe(780); // 840 + (800 - 860)

    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 860 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("780");
  });

  it("ドラッグ中の幅も 320–1600px に clamp する", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800 });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: -2000 });
    expect(width()).toBe(1600);
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 3000 });
    expect(width()).toBe(320);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 3000 });
    expect(localStorage.getItem("fanout.drawerWidth")).toBe("320");
  });

  it("ドラッグ外の pointermove・別 pointerId の move は無視する", () => {
    render(<Probe />);
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 100 });
    expect(width()).toBe(840);

    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800 });
    fireEvent.pointerMove(grip(), { pointerId: 2, clientX: 100 });
    expect(width()).toBe(840);
    fireEvent.pointerUp(grip(), { pointerId: 1, clientX: 800 });
  });

  it("pointercancel はドラッグを終了するが永続化しない", () => {
    render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800 });
    fireEvent.pointerMove(grip(), { pointerId: 1, clientX: 700 });
    fireEvent.pointerCancel(grip(), { pointerId: 1 });
    expect(document.documentElement).not.toHaveClass("drawer-resizing");
    expect(localStorage.getItem("fanout.drawerWidth")).toBeNull();
  });

  it("ドラッグ中の unmount で html.drawer-resizing を残さない", () => {
    const probe = render(<Probe />);
    fireEvent.pointerDown(grip(), { button: 0, pointerId: 1, clientX: 800 });
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
});
