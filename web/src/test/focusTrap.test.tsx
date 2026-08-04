import { fireEvent, render } from "@testing-library/react";
import { useRef } from "react";
import { describe, expect, it } from "vitest";
import { useFocusTrap } from "../shared/useFocusTrap";

/* 非表示要素を境界にすると、最後に「見えている」要素から Tab を押しても
 * current === last が成立せず、そのままブラウザ UI へ抜ける。diff ビュアーの
 * サイドバーは container query で display:none になるので実際に起こる。 */
function Probe({ hideLast }: { hideLast: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref, true);
  return (
    <div ref={ref} tabIndex={-1}>
      <button id="first">first</button>
      <button id="middle">middle</button>
      <button id="last" data-hidden={hideLast ? "" : undefined}>
        last
      </button>
    </div>
  );
}

/* jsdom は checkVisibility を実装しないので、hook は「答えられない環境では
 * 弾かない」側に倒れる。ここでは checkVisibility を生やして可視性判定そのものを
 * 検証する(実ブラウザと同じ経路を通す)。 */
function stubCheckVisibility() {
  const proto = HTMLElement.prototype as unknown as { checkVisibility?: () => boolean };
  const had = "checkVisibility" in proto;
  proto.checkVisibility = function (this: HTMLElement) {
    return !this.hasAttribute("data-hidden");
  };
  return () => {
    if (!had) delete proto.checkVisibility;
  };
}

describe("useFocusTrap", () => {
  it("末尾から Tab で先頭へ、先頭から Shift+Tab で末尾へ回る", () => {
    render(<Probe hideLast={false} />);
    const first = document.getElementById("first")!;
    const last = document.getElementById("last")!;

    last.focus();
    fireEvent.keyDown(last, { key: "Tab" });
    expect(document.activeElement).toBe(first);

    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(last);
  });

  it("非表示の要素は境界にしない", () => {
    const restore = stubCheckVisibility();
    try {
      render(<Probe hideLast />);
      const first = document.getElementById("first")!;
      const middle = document.getElementById("middle")!;

      // 見えている最後の要素は middle。ここからの Tab が先頭へ回る
      middle.focus();
      fireEvent.keyDown(middle, { key: "Tab" });
      expect(document.activeElement).toBe(first);
    } finally {
      restore();
    }
  });
});
