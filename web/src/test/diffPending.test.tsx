import { fireEvent, render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { DiffPending } from "../components/App";

/* diff オーバーレイは lazy chunk。解決を待つあいだ Suspense の fallback として
 * これが立つ。空 fallback にすると、その窓での Escape は(Drawer 起点なら)
 * Drawer だけを閉じて diffTarget が残り、chunk が解決した瞬間に「閉じたはず」の
 * diff が出てくる。表セル起点では Escape 自体が効かない。 */
describe("DiffPending", () => {
  it("解決待ちの Escape で起動を取り消し、背面へは渡さない", () => {
    const onCancel = vi.fn();
    const bubbled = vi.fn();
    document.addEventListener("keydown", bubbled); // Drawer の document listener 相当
    try {
      render(<DiffPending enabled onCancel={onCancel} />);
      fireEvent.keyDown(document.body, { key: "Escape" });

      expect(onCancel).toHaveBeenCalledTimes(1);
      // capture 段で preventDefault するので、後段は defaultPrevented を見て降りる
      expect(bubbled.mock.calls[0]?.[0].defaultPrevented).toBe(true);
    } finally {
      document.removeEventListener("keydown", bubbled);
    }
  });

  it("設定モーダルが上にある間は Escape を譲る", () => {
    const onCancel = vi.fn();
    render(<DiffPending enabled={false} onCancel={onCancel} />);
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(onCancel).not.toHaveBeenCalled();
  });

  it("他が消費済みの Escape は取らない", () => {
    const onCancel = vi.fn();
    render(<DiffPending enabled onCancel={onCancel} />);
    const e = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    e.preventDefault();
    document.body.dispatchEvent(e);
    expect(onCancel).not.toHaveBeenCalled();
  });
});
