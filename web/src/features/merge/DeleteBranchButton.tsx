import { useLingui } from "@lingui/react/macro";
import { useState } from "react";
import { postJson } from "../../transport/api";
import type { PRRef } from "../../transport/types";
import { deleteBranchErrorMessage, DELETE_BRANCH_PATH } from "./merge";

/* マージ済み PR の head branch を消すボタン。GitHub 自身の "Delete branch" と
 * 同じで、マージが終わってから現れる別操作にしてある。
 *
 * 分けたことで、ここは状態をほとんど持たない: ref の削除は冪等(既に無い = 完了)
 * なので、マージ側が必要とする「曖昧な mutation を二度撃たない」仕掛けが一切
 * 要らない。押した後に消えるのはこのセッション内の表示上の都合で、消えた ref を
 * もう一度消しても成功する。 */
export function DeleteBranchButton({
  id,
  pr,
  query,
  token,
  blockedBy,
}: {
  id: string;
  pr: PRRef;
  query: Record<string, string>;
  token: string;
  /* この branch を base にしている open PR(prBasedOn)。ある間はサーバが 409
   * branch_is_base を返すので、押せる見た目にしない。 */
  blockedBy: number | null;
}) {
  const { i18n, t } = useLingui();
  const [state, setState] = useState<"idle" | "sending" | "done" | "error">("idle");
  const [error, setError] = useState("");

  if (state === "done") return null;

  const blocked = blockedBy
    ? t`#${{ pr: blockedBy }} がこの branch を base にしているため削除できません`
    : "";

  const run = () => {
    if (state === "sending" || blocked) return;
    setState("sending");
    void postJson(DELETE_BRANCH_PATH, token, {
      params: query,
      body: { prNumber: pr.number, headSha: pr.headSha ?? "" },
    })
      .then(async (res) => {
        if (res.ok) {
          setState("done");
          return;
        }
        setError(i18n._(await deleteBranchErrorMessage(res)));
        setState("error");
      })
      .catch(() => {
        setError(t`ブランチを削除できませんでした(接続エラー)`);
        setState("error");
      });
  };

  const label = blocked || t`${{ branch: pr.headRef ?? "" }} を削除`;
  return (
    <span className="d-branch-delete">
      <button
        type="button"
        id={id}
        className="btn-quiet tip"
        aria-disabled={blocked ? true : undefined}
        aria-label={blocked ? t`ブランチを削除 — ${{ reason: blocked }}` : undefined}
        data-tip={label}
        onClick={run}
      >
        {state === "sending" ? t`削除中…` : t`ブランチを削除`}
      </button>
      {state === "error" && (
        <span className="d-branch-delete-error" role="alert">
          {error}
        </span>
      )}
    </span>
  );
}
