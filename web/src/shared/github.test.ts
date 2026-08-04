import { describe, expect, it } from "vitest";
import { ghBase, issueUrl, parentLabel, parentUrl, prUrl } from "./github";

describe("ghBase", () => {
  it("owner/name 形式のみ URL 化する", () => {
    expect(ghBase("octo/fanout")).toBe("https://github.com/octo/fanout");
    expect(ghBase("octo/fan.out-v2")).toBe("https://github.com/octo/fan.out-v2");
  });

  it("不正形式(空・パス注入)は空文字 — リンク化しない", () => {
    expect(ghBase("")).toBe("");
    expect(ghBase("octo")).toBe("");
    expect(ghBase("octo/fanout/extra")).toBe("");
    expect(ghBase("octo/fanout?x=1")).toBe("");
    expect(ghBase("https://evil.example/octo/fanout")).toBe("");
  });
});

describe("issueUrl / prUrl", () => {
  it("n > 0 のみリンク化(@manual の合成負番号はリンクにしない)", () => {
    expect(issueUrl("octo/fanout", 12)).toBe("https://github.com/octo/fanout/issues/12");
    expect(issueUrl("octo/fanout", -1)).toBe("");
    expect(issueUrl("octo/fanout", 0)).toBe("");
    expect(prUrl("octo/fanout", 7)).toBe("https://github.com/octo/fanout/pull/7");
  });

  it("repo 未解決時は空", () => {
    expect(issueUrl("", 12)).toBe("");
  });
});

describe("parentUrl / parentLabel", () => {
  it("数値 parent は issue URL・# 付きラベル", () => {
    expect(parentUrl("octo/fanout", "142")).toBe("https://github.com/octo/fanout/issues/142");
    expect(parentLabel("142")).toBe("#142");
  });

  it("github.com URL はプレフィックス検証つきパススルー・短縮ラベル", () => {
    const url = "https://github.com/orgs/octo/projects/3";
    expect(parentUrl("octo/fanout", url)).toBe(url);
    expect(parentLabel(url)).toBe("orgs/octo/projects/3");
  });

  it("それ以外の parent はリンク化しない", () => {
    expect(parentUrl("octo/fanout", "https://evil.example/x")).toBe("");
    expect(parentUrl("octo/fanout", "javascript:alert(1)")).toBe("");
  });
});
