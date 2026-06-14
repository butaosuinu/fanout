import { describe, expect, it } from "vitest";
import { makePane } from "../test/fixtures";
import { paneLabel, rowKey } from "./pane";

describe("pane identity helpers", () => {
  it("keys issue rows by parent and issue number", () => {
    const pane = makePane({ issueNum: 42 });

    expect(rowKey("100", pane)).toBe("100#42");
    expect(paneLabel(pane)).toBe("#42");
  });

  it("keys task rows by parent and task id", () => {
    const pane = makePane({ issueNum: 0, taskId: "api-client" });

    expect(rowKey("plan:launch-plan", pane)).toBe("plan:launch-plan@api-client");
    expect(paneLabel(pane)).toBe("api-client");
  });
});
