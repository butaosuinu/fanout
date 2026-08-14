import { describe, expect, it } from "vitest";
import { makePane, makePr, makeQueuedPane } from "../../test/fixtures";
import { diffQuery, rowQuery } from "../sessions/pane";
import { mergeBlockReason, mergeTargetPr, mergeWarnings } from "./merge";

const OK = { githubDegraded: false, pending: false, tokenless: false };

describe("mergeBlockReason", () => {
  it("PR が無い行は塞ぐ", () => {
    expect(mergeBlockReason(null, OK)).not.toBeNull();
  });

  it("open で mergeable な PR は通す", () => {
    expect(mergeBlockReason(makePr(), OK)).toBeNull();
  });

  it("state が MERGED なら塞ぐ", () => {
    expect(mergeBlockReason(makePr({ state: "MERGED" }), OK)).not.toBeNull();
  });

  /* Go 側 DisplayState と同じく mergedAt 単独でも merged 扱い。 */
  it("mergedAt だけでも merged として塞ぐ", () => {
    expect(mergeBlockReason(makePr({ mergedAt: "2026-08-15T00:00:00Z" }), OK)).not.toBeNull();
  });

  it("closed を塞ぐ", () => {
    expect(mergeBlockReason(makePr({ state: "CLOSED" }), OK)).not.toBeNull();
  });

  it("draft を塞ぐ", () => {
    expect(mergeBlockReason(makePr({ isDraft: true }), OK)).not.toBeNull();
  });

  it("CONFLICTING を塞ぐ", () => {
    expect(mergeBlockReason(makePr({ mergeable: "CONFLICTING" }), OK)).not.toBeNull();
  });

  /* 欠落は「不明」であって「衝突あり」ではない。塞ぐと GitHub の再計算中に
   * 正当なマージができなくなる。 */
  it("mergeable 欠落は塞がない", () => {
    expect(mergeBlockReason(makePr({ mergeable: undefined }), OK)).toBeNull();
  });

  it("GitHub が degraded なら塞ぐ", () => {
    expect(mergeBlockReason(makePr(), { ...OK, githubDegraded: true })).not.toBeNull();
  });

  it("送信済みで反映待ちの間は塞ぐ", () => {
    expect(mergeBlockReason(makePr(), { ...OK, pending: true })).not.toBeNull();
  });

  /* サーバは token gate が無いと 403 を返すので、押すたびに失敗させない。 */
  it("token 無効のダッシュボードでは塞ぐ", () => {
    expect(mergeBlockReason(makePr(), { ...OK, tokenless: true })).not.toBeNull();
  });

  /* 判定順は prDisplayState と揃える — merged が draft や conflict に勝つ。 */
  it("merged は draft と conflict より先に決まる", () => {
    const pr = makePr({ state: "MERGED", isDraft: true, mergeable: "CONFLICTING" });
    expect(mergeBlockReason(pr, OK)?.message).toBe(
      mergeBlockReason(makePr({ state: "MERGED" }), OK)?.message,
    );
  });

  it("レビュー未承認と CI 失敗は塞がない", () => {
    expect(mergeBlockReason(makePr({ reviewDecision: "REVIEW_REQUIRED" }), OK)).toBeNull();
    expect(mergeBlockReason(makePr({ reviewDecision: "CHANGES_REQUESTED" }), OK)).toBeNull();
    expect(mergeBlockReason(makePr({ ci: "fail" }), OK)).toBeNull();
  });
});

describe("mergeWarnings", () => {
  it("承認済み・CI 通過・mergeable な PR には警告が無い", () => {
    expect(mergeWarnings(makePr({ reviewDecision: "APPROVED", ci: "pass" }))).toEqual([]);
  });

  it("変更要求を警告する", () => {
    expect(mergeWarnings(makePr({ reviewDecision: "CHANGES_REQUESTED" }))).toHaveLength(1);
  });

  it("レビュー未承認を警告する", () => {
    expect(mergeWarnings(makePr({ reviewDecision: "REVIEW_REQUIRED" }))).toHaveLength(1);
  });

  it("CI の失敗と未完了を警告する", () => {
    expect(mergeWarnings(makePr({ ci: "fail" }))).toHaveLength(1);
    expect(mergeWarnings(makePr({ ci: "pending" }))).toHaveLength(1);
  });

  it("競合の不明を警告する", () => {
    expect(mergeWarnings(makePr({ mergeable: undefined }))).toHaveLength(1);
  });

  it("複数の理由をすべて挙げる", () => {
    const pr = makePr({ reviewDecision: "REVIEW_REQUIRED", ci: "fail", mergeable: undefined });
    expect(mergeWarnings(pr)).toHaveLength(3);
  });
});

describe("mergeTargetPr", () => {
  const REPO = "octo/fanout";

  it("merged PR と open PR が並ぶ行では open を採る", () => {
    const prs = [makePr({ number: 700, state: "MERGED" }), makePr({ number: 701 })];
    expect(mergeTargetPr(prs, REPO, "")?.number).toBe(701);
  });

  it("open が無ければ所有している先頭に落ちる", () => {
    const prs = [makePr({ number: 700, state: "MERGED" })];
    expect(mergeTargetPr(prs, REPO, "")?.number).toBe(700);
  });

  /* 別 repository を base にする PR は番号だけ渡すと別 PR を掴む。 */
  it("別 repository の PR は対象にしない", () => {
    const prs = [makePr({ number: 700, baseRepo: "other/repo" })];
    expect(mergeTargetPr(prs, REPO, "")).toBeNull();
  });

  /* branch 行の PR は head branch 名でしか結び付いていないので、fork が先頭に
   * 並ぶと正当な PR に手が届かなくなる。 */
  it("branch 行では fork の同名 PR を飛ばして自分の PR を採る", () => {
    const prs = [
      makePr({ number: 700, headRepo: "stranger/fork", headRef: "fanout/fix-thing" }),
      makePr({ number: 701, headRepo: REPO, headRef: "fanout/fix-thing" }),
    ];
    expect(mergeTargetPr(prs, REPO, "fanout/fix-thing")?.number).toBe(701);
  });

  it("branch 行では head branch 名の違う PR を対象にしない", () => {
    const prs = [makePr({ number: 700, headRef: "other" })];
    expect(mergeTargetPr(prs, REPO, "fanout/fix-thing")).toBeNull();
  });

  /* issue 行は closing-PR link が identity なので fork PR も正当。 */
  it("issue 行では fork PR も対象にする", () => {
    const prs = [makePr({ number: 700, headRepo: "stranger/fork" })];
    expect(mergeTargetPr(prs, REPO, "")?.number).toBe(700);
  });
});

describe("rowQuery", () => {
  /* merge は worktree の実体を要らない — cleanup 済みでも PR はマージできる。 */
  it("worktree の記録が無くても identity を返す", () => {
    const pane = makePane({ worktreePath: "" });
    expect(rowQuery("575", pane)).toEqual({ parent: "575", issue: "101" });
    expect(diffQuery("575", pane)).toBeNull();
  });

  it("plan task 行は sourceKey を要求する", () => {
    const base = { issueNum: -1, taskId: "t1" };
    expect(rowQuery("plan:x", makePane({ ...base, sourceKey: "k" }))).toEqual({
      parent: "plan:x",
      task: "t1",
      source: "k",
    });
    expect(rowQuery("plan:x", makePane(base))).toBeNull();
  });

  it("負の synthetic issue 行は sourceKey を要求する", () => {
    expect(rowQuery("@manual", makePane({ issueNum: -2, sourceKey: "k" }))).toEqual({
      parent: "@manual",
      issue: "-2",
      source: "k",
    });
    expect(rowQuery("@manual", makePane({ issueNum: -2 }))).toBeNull();
  });

  it("shell 行と未開始行は identity を持たない", () => {
    expect(rowQuery("575", makePane({ kind: "shell" }))).toBeNull();
    expect(rowQuery("575", makeQueuedPane())).toBeNull();
  });
});
