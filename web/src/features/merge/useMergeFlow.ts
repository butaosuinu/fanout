import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { useCallback } from "react";
import type { PaneView, PRRef, Snapshot } from "../../transport/types";
import { useMergePr, type MergeState } from "../../transport/useMergePr";
import type { MergeMethod } from "../settings/useSettings";
import { rowKey, rowQuery } from "../sessions/pane";
import { useMergeTracking, type MergeTracking, type Notice } from "./useMergeRelease";
import { usePinnedHead, type DiffSource } from "./usePinnedHead";
import { mergeBlockReason, mergeTargetPr, mergeWarnings } from "./merge";

/* 1 行ぶんのマージボタンの状態。Drawer と diff ツールバーが同じものを受け取る。 */
export interface MergeAffordance {
  prNumber: number;
  /* 対象 PR の head commit。diff ビュアーは開いた時点の値を pin して、ズレたら
   * マージを塞ぐ(usePinnedHead)。 */
  headSha: string;
  /* 対象 PR の head と base。diff ビュアーが読んでいる worktree と同じものかを
   * 判定する。 */
  headRef: string;
  headRepo: string;
  baseRef: string;
  blocked: MessageDescriptor | null;
  warnings: MessageDescriptor[];
  /* 直近の失敗。送信した行にだけ出す — 確認ダイアログを持たない導線なので、
   * 結果を返す場所がボタンの隣しかない。 */
  error: MessageDescriptor | null;
  /* マージ自体は成功したが、後片付け(remote branch 削除)が完了しなかった、
   * のような非致命の報告。 */
  notice: MessageDescriptor | null;
  sending: boolean;
  onMerge: (method: MergeMethod) => void;
}

type Target = { key: string; query: Record<string, string>; pr: PRRef };

export function useMergeFlow(
  snap: Snapshot | null,
  token: string,
  /* 開いている diff の対象行。その行のマージは、開いた時点の head に固定する。 */
  diffKey: string | null,
): { affordanceFor: (parent: string, pane: PaneView) => MergeAffordance | null } {
  const { state, submit, busy } = useMergePr(token);
  /* 送信結果の追跡(どの行が反映待ちか・どの PR が結果不明か)は 1 か所に持つ。 */
  const track = useMergeTracking(snap);
  const pinDiffHead = usePinnedHead(diffKey);

  const run = useCallback(
    (target: Target) => (method: MergeMethod) => {
      /* 別の行の送信中なら撃たない。ここで帰属先を進めてしまうと、前の行の
       * 「マージ中…」やエラーがこの行のボタンに付け替わって見える。 */
      if (busy()) return;
      const row = { key: target.key, prNumber: target.pr.number };
      track.begin(row.key);
      void submit(mergeRequestFor(target, method)).then((res) => {
        if (res.ok) track.apply(row, res);
      });
    },
    [submit, busy, track],
  );

  const affordanceFor = useCallback(
    (parent: string, pane: PaneView): MergeAffordance | null => {
      const repo = snap?.repo ?? "";
      const pr = mergeTargetPr(pane.prs, repo, branchOf(pane));
      const query = rowQuery(parent, pane);
      /* PR も identity も無い行にはボタンごと出さない。無効なボタンを並べても
       * 「いつかマージできる行」ではないので、情報が増えない。 */
      if (!pr || !query) return null;
      const key = rowKey(parent, pane);
      return pinDiffHead(
        key,
        buildAffordance({
          pr,
          githubDegraded: snap?.degraded?.github === true,
          tokenless: token === "",
          pendingHere: heldBack({ key, prNumber: pr.number }, track),
          ...resultsFor(key, track, state),
          onMerge: run({ key, query, pr }),
        }),
        diffSource(pane, repo),
      );
    },
    [snap, track, run, state, token, pinDiffHead],
  );

  return { affordanceFor };
}

/* この行のボタンを塞ぐか。反映待ちは行単位、結果不明は PR 単位 — 同じ PR が
 * 複数行に載る場合に、別の行から不明なまま撃ち直せないようにする。 */
function heldBack(row: { key: string; prNumber: number }, track: MergeTracking): boolean {
  return track.pending?.key === row.key || track.unknown?.prNumber === row.prNumber;
}

/* branch-backed 行だけ head branch まで所有権を要求する(サーバの VerifyRowOwns
 * と同じ切り分け)。issue 行は closing-PR link が identity なので "" を渡す。 */
function branchOf(pane: PaneView): string {
  return pane.issueNum > 0 ? "" : (pane.branchName ?? "");
}

/* 送信結果はその行のボタンにだけ返す。別の行のボタンに他の行のエラーや
 * 「マージ中…」が付くと、どの操作の結果なのか読めなくなる。 */
function resultsFor(
  key: string,
  track: MergeTracking,
  state: MergeState,
): { state: MergeState | null; notice: Notice } {
  if (track.lastKey !== key) return { state: null, notice: null };
  return { state, notice: track.notice?.key === key ? track.notice : null };
}

function mergeRequestFor(target: Target, method: MergeMethod) {
  const { prNumber, headSha, baseRef } = prIdentity(target.pr);
  return { query: target.query, method, prNumber, headSha, baseRef };
}

/* 対象 PR の identity。ボタンが撃つ先であり、diff の pin が照合する相手でもある
 * ので、1 か所から配る。欠落は "" — 送らない値と「空」を区別しない。 */
function prIdentity(pr: PRRef) {
  return {
    prNumber: pr.number,
    headSha: pr.headSha ?? "",
    headRef: pr.headRef ?? "",
    headRepo: pr.headRepo ?? "",
    baseRef: pr.baseRef ?? "",
  };
}

/* diff ビュアーがこの行で読んでいる worktree。 */
function diffSource(pane: PaneView, repo: string): DiffSource {
  return { repo, branch: pane.branchName ?? "", base: pane.baseBranch ?? "" };
}

function buildAffordance(input: {
  pr: PRRef;
  githubDegraded: boolean;
  tokenless: boolean;
  pendingHere: boolean;
  /* 送信した行のときだけ非 null。他の行には結果を出さない。 */
  state: MergeState | null;
  notice: Notice;
  onMerge: (method: MergeMethod) => void;
}): MergeAffordance {
  const blocked = mergeBlockReason(input.pr, {
    githubDegraded: input.githubDegraded,
    tokenless: input.tokenless,
    pending: input.pendingHere,
  });
  return {
    ...prIdentity(input.pr),
    blocked,
    /* 警告は「押せる操作をためらう理由」なので、押せない行では出さない。
     * merged PR は mergeable が常に欠落するため、そうしないと「競合の有無が
     * 不明」がマージ済みの行に必ず付く。 */
    warnings: blocked ? [] : mergeWarnings(input.pr),
    error: input.state?.phase === "error" ? input.state.message : null,
    notice: noticeMessage(input.notice),
    sending: input.state?.phase === "sending",
    onMerge: input.onMerge,
  };
}

function noticeMessage(notice: Notice): MessageDescriptor | null {
  if (!notice) return null;
  if (notice.kind === "queued") {
    return msg`GitHub がマージを受け付けました(merge queue 待ち)。まだマージされていません`;
  }
  if (notice.kind === "unknown") {
    return msg`マージの結果を確認できませんでした。GitHub 側の状態を確認してください`;
  }
  return null;
}
