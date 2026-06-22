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

  it("disambiguates worktree-local rows by sourceKey", () => {
    const a = makePane({ issueNum: 0, taskId: "api", sourceKey: "aaaa" });
    const b = makePane({ issueNum: 0, taskId: "api", sourceKey: "bbbb" });

    // Same plan task in two worktrees must not collide on the row key.
    expect(rowKey("plan:launch", a)).toBe("plan:launch@api~aaaa");
    expect(rowKey("plan:launch", b)).toBe("plan:launch@api~bbbb");
    expect(rowKey("plan:launch", a)).not.toBe(rowKey("plan:launch", b));

    // GitHub issue rows carry no sourceKey, so the key is unchanged.
    expect(rowKey("100", makePane({ issueNum: 42 }))).toBe("100#42");
  });
});
