import { render, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { beforeEach, describe, expect, it } from "vitest";
import { makeDiffResponse } from "../test/fixtures";
import { server } from "../test/server";
import { useDiff } from "./useDiff";

/* 取得済み diff を url と結び付けずに持つと、対象を切り替えた render で前の
 * ready がまだ残る(loading 化は passive effect なので 1 コミット遅れる)。
 * その 1 コミットで、新しい対象のタイトルと前の対象の patch が同時に描かれる。
 *
 * userEvent / rerender は act の中で effect を流してしまうので、その render は
 * 統合テストからは観測できない。render ごとの (url, state) を記録して直接見る。 */

type Seen = { url: string; phase: string; patch?: string };

function Probe({ url, seen }: { url: string; seen: Seen[] }) {
  const { state } = useDiff(url);
  seen.push({
    url,
    phase: state.phase,
    patch: state.phase === "ready" ? state.diff.patch : undefined,
  });
  return null;
}

beforeEach(() => {
  server.use(
    http.get("/api/diff", ({ request }) => {
      const issue = new URL(request.url).searchParams.get("issue") ?? "?";
      return HttpResponse.json(makeDiffResponse({ patch: `patch_of_${issue}` }));
    }),
  );
});

describe("useDiff", () => {
  it("url が変わった render では前の diff を返さない", async () => {
    const seen: Seen[] = [];
    const { rerender } = render(<Probe url="/api/diff?issue=1" seen={seen} />);
    await waitFor(() => {
      expect(seen.at(-1)).toMatchObject({ phase: "ready", patch: "patch_of_1" });
    });

    rerender(<Probe url="/api/diff?issue=2" seen={seen} />);
    await waitFor(() => {
      expect(seen.at(-1)).toMatchObject({ phase: "ready", patch: "patch_of_2" });
    });

    // どの render でも「url と patch の対象」が食い違わないこと
    const mismatched = seen.filter(
      (s) => s.patch !== undefined && !s.url.endsWith(s.patch.slice(-1)),
    );
    expect(mismatched).toEqual([]);
  });
});
