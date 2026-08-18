import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { errorBody } from "../../transport/api";
import type { PRRef } from "../../transport/types";
import type { MergeMethod } from "../settings/useSettings";

/* サーバ契約の唯一の接点。エンドポイントと body の形が変わるならここだけを
 * 直す — 呼び出し側は記号でしか参照しない。正は docs/architecture.ja.md の
 * 不変条件カタログ(dashboard 節)。 */
export const MERGE_PATH = "/api/pr/merge";
export const DELETE_BRANCH_PATH = "/api/pr/delete-branch";

export interface MergeRequest {
  query: Record<string, string>;
  method: MergeMethod;
  prNumber: number;
  /* 描画時に見ていた head commit。サーバがこれを --match-head-commit として
   * GitHub に渡すので、ページを開いてから押すまでに push された PR は
   * マージされずに拒否される。GraphQL 経路以外の PR では欠落しうる。 */
  headSha: string;
  /* 描画時のマージ先 branch。retarget を捕まえるために送り返す。 */
  baseRef: string;
}

export function mergeRequestBody(req: MergeRequest): Record<string, unknown> {
  return {
    prNumber: req.prNumber,
    headSha: req.headSha,
    baseRef: req.baseRef,
    method: req.method,
  };
}

function prState(pr: PRRef): string {
  return (pr.state ?? "").trim().toUpperCase();
}

function isMerged(pr: PRRef): boolean {
  return prState(pr) === "MERGED" || !!pr.mergedAt;
}

function isOpen(pr: PRRef): boolean {
  return !isMerged(pr) && prState(pr) !== "CLOSED";
}

/* サーバの Preflight を通る見込みがあるか。行に複数 open PR があるとき、先頭が
 * draft や CONFLICTING だとそれだけが選ばれて唯一のボタンが永久に無効になる。 */
function actionable(pr: PRRef): boolean {
  if (!isOpen(pr) || pr.isDraft || pr.mergeable === "CONFLICTING") return false;
  return !pr.autoMerge && !pr.queued;
}

/* サーバの VerifyRowOwns と同じ所有権規則。ここで揃えないと、行に載った他人の
 * fork PR を対象に選んでしまい、唯一のボタンが 409 を返すだけになって正当な PR に
 * 手が届かない。 */
function ownedByRow(pr: PRRef, repo: string, branch: string): boolean {
  if (!pr.baseRepo || pr.baseRepo.toLowerCase() !== repo.toLowerCase()) return false;
  if (!branch) return true; // issue 行は closing-PR link が identity(fork も正当)
  return pr.headRepo?.toLowerCase() === repo.toLowerCase() && pr.headRef === branch;
}

/* マージ対象の PR。prPrimary は使わない — あちらは「最初の MERGED を優先」で、
 * マージ操作には意味論が逆になる(古い merged PR と生きた open PR が同じ行に
 * 並ぶと、永久に無効なボタンを描いてしまう)。サーバの prmerge.SelectRef が
 * 同じ理由で PrimaryPR を避けているので、こちらも open を先に採る。open が
 * 無いときは、所有している中の先頭に落ちる。
 *
 * branch は branch-backed 行の記録 branch、issue 行では ""。 */
export function mergeTargetPr(
  prs: PRRef[] | null | undefined,
  repo: string,
  branch: string,
): PRRef | null {
  const owned = (prs ?? []).filter((pr) => ownedByRow(pr, repo, branch));
  return owned.find(actionable) ?? owned.find(isOpen) ?? owned[0] ?? null;
}

/* PR そのものを見て押せないと分かる状態。判定順は prDisplayState と揃える —
 * merged / closed / draft が conflict より先に決まる。CONFLICTING だけが
 * 「衝突あり」で、欠落は「不明」なので塞がず mergeWarnings に回す。 */
const PR_BLOCKS: { when: (pr: PRRef) => boolean; reason: MessageDescriptor }[] = [
  { when: isMerged, reason: msg`この PR は既にマージ済みです` },
  { when: (pr) => prState(pr) === "CLOSED", reason: msg`この PR は close 済みです` },
  {
    when: (pr) => !!pr.isDraft,
    reason: msg`draft PR はマージできません(Ready for review にしてください)`,
  },
  { when: (pr) => pr.mergeable === "CONFLICTING", reason: msg`base branch と競合しています` },
  /* GitHub が既にこの PR のマージを保持している(auto-merge 武装済み、または
   * merge queue 内)。もう一度送っても早くはならない。 */
  {
    when: (pr) => !!pr.autoMerge || !!pr.queued,
    reason: msg`GitHub がこの PR のマージを保留中です`,
  },
];

/* 押せない理由。押せるなら null。 */
export function mergeBlockReason(
  pr: PRRef | null,
  opts: { githubDegraded: boolean; pending: boolean; tokenless: boolean },
): MessageDescriptor | null {
  if (opts.pending) return msg`マージを送信しました。GitHub への反映を待っています`;
  if (!pr) return msg`マージ対象の PR がありません`;
  /* サーバは token gate が無いとマージを拒否する。押すたびに 403 になるより、
   * 押せない理由をここで見せる。 */
  if (opts.tokenless) return msg`--no-token で起動したダッシュボードではマージできません`;
  if (opts.githubDegraded) return msg`GitHub の情報を取得できていません`;
  return PR_BLOCKS.find((b) => b.when(pr))?.reason ?? null;
}

/* 押せるが、押す前に知っておくべき状態。
 *
 * レビュー承認と CI をここに置いて無効化に回さないのは、それらがマージを止める
 * かどうかが branch protection の設定次第だから。web 側は設定を知り得ないので
 * 「事前判定できる」とは言えず、塞ぐと保護ルールの無い repo で正当な操作が
 * できなくなる。実際の拒否は 422 のエラー表示で受ける。 */
export function mergeWarnings(pr: PRRef): MessageDescriptor[] {
  const out: MessageDescriptor[] = [];
  const decision = (pr.reviewDecision ?? "").trim().toUpperCase();
  if (decision === "CHANGES_REQUESTED") out.push(msg`レビューで変更が要求されています`);
  if (decision === "REVIEW_REQUIRED") out.push(msg`レビュー未承認です`);
  const ci = (pr.ci ?? "").trim().toLowerCase();
  if (ci === "fail") out.push(msg`CI が失敗しています`);
  if (ci === "pending") out.push(msg`CI がまだ完了していません`);
  if (!pr.mergeable) out.push(msg`競合の有無を GitHub 側で確認できていません`);
  return out;
}

/* ステータス別の文言。detail はサーバの {"error"} 本文で、敵性入力として
 * テキストノードにだけ入れる。405 は実在ケース — merge を知らない旧バイナリの
 * getOnly が返す。 */
const MERGE_ERRORS: Record<number, (detail: string) => MessageDescriptor> = {
  403: () => msg`マージする権限がありません(token を確認してください)`,

  404: () => msg`マージ対象を特定できません(行の記録が見つかりません)`,
  405: () => msg`このサーバーは merge に対応していません(ダッシュボードを再起動してください)`,
  409: (detail) => (detail ? msg`マージできません: ${{ detail }}` : msg`マージできません`),
  422: (detail) =>
    detail ? msg`GitHub がマージを拒否しました: ${{ detail }}` : msg`GitHub がマージを拒否しました`,
  429: () => msg`GitHub の API 制限に達しています。しばらく待って再試行してください`,
  502: () => msg`GitHub に接続できませんでした`,
  503: () => msg`このダッシュボードは merge を実行できません`,
  504: () => msg`マージがタイムアウトしました。GitHub 側の状態を確認してください`,
};

/* sameOriginOnly の 403。token ではなく URL の問題なので、token を疑わせない。
 * 素直に起きるのは、表示された 127.0.0.1 の URL ではなく localhost で開いた
 * ときで、サーバは Host の完全一致を要求する(DNS rebinding のピン)。 */
const SAME_ORIGIN_CODES = new Set(["host", "origin", "site"]);

export async function mergeErrorMessage(res: Response): Promise<MessageDescriptor> {
  const { detail, code } = await errorBody(res);
  if (res.status === 403 && SAME_ORIGIN_CODES.has(code)) {
    return msg`この URL からはマージできません(表示された 127.0.0.1 の URL で開き直してください)`;
  }
  const build = MERGE_ERRORS[res.status];
  if (build) return build(detail);
  return detail
    ? msg`マージに失敗しました (HTTP ${{ status: res.status }}): ${{ detail }}`
    : msg`マージに失敗しました (HTTP ${{ status: res.status }})`;
}

/* マージ済みの行にだけ出す、後片付けボタンの出せる条件。サーバの admission
 * (mergeEnabled + PlanDelete)をそのまま写す — ここが緩いと、押すたびに必ず
 * 403 / 409 になるボタンを表示してしまう。
 *
 * - token が無ければサーバは必ず 403(--no-token では mutation を開けない)
 * - head branch が分かっていて、この repository のものであること(fork の head を
 *   base 側の同名 branch として消さないため)
 * - 記録 branch があるならそれと一致すること(サーバの PlanDelete は不一致を
 *   必ず 409 にする) */
export function canDeleteBranch(
  pr: PRRef | null,
  ctx: { repo: string; branch: string; token: string },
): boolean {
  if (!pr || !ctx.token) return false;
  if (!isMerged(pr) || !pr.headSha) return false;
  return headOwnedByRow(pr, ctx.repo, ctx.branch);
}

/* サーバの PlanDelete と同じ head の所有権規則。 */
function headOwnedByRow(pr: PRRef, repo: string, branch: string): boolean {
  if (!pr.headRef || pr.headRepo?.toLowerCase() !== repo.toLowerCase()) return false;
  return !branch || pr.headRef === branch;
}

export async function deleteBranchErrorMessage(res: Response): Promise<MessageDescriptor> {
  const { detail } = await errorBody(res);
  if (res.status === 409) {
    return detail
      ? msg`ブランチを削除できませんでした: ${{ detail }}`
      : msg`ブランチを削除できませんでした`;
  }
  return detail
    ? msg`ブランチの削除に失敗しました (HTTP ${{ status: res.status }}): ${{ detail }}`
    : msg`ブランチの削除に失敗しました (HTTP ${{ status: res.status }})`;
}

export const MERGE_NETWORK_ERROR = msg`マージに失敗しました(接続エラー)`;

/* 方式のラベル。モジュール定数は import 時に一度だけ評価されるので、翻訳済み
 * 文字列ではなく descriptor を置き、描画時に i18n._() で解決する。 */
/* diff ビュアーが描いた patch と、これからマージされる head がズレたときの理由。
 * diff は明示的な再取得まで最初の patch を保持するので、その間に agent が push
 * すると、3 段照合はすべて新しい head で一致してしまい「見ていない変更」が
 * 通る。ズレている間はマージを塞ぐ。 */
/* 表示中の patch と対象 PR が別物のときの理由。/api/diff は pane の worktree を
 * 読むので、その branch を head に持たない PR(fork の closing PR など)を
 * マージすると、画面に無い変更が入る。 */
export const MERGE_DIFF_MISMATCH = msg`表示中の差分はこの PR のものではありません。PR のページで確認してください`;

/* 表示中の patch に、マージされない変更が混ざっているときの理由。diff は worktree の
 * 差分なので未コミットの変更も描くが、GitHub に入るのは commit 済みまで。 */
export const MERGE_DIFF_UNCOMMITTED = msg`表示中の差分に未コミットの変更が含まれています。commit して push してから実行してください`;

/* 差分をまだ取得できていないときの理由。表示中の patch が無いのだから、それと
 * 突き合わせて安全だとも言えない。 */
export const MERGE_DIFF_UNKNOWN = msg`表示中の差分を取得できていません。再取得してから実行してください`;

/* 表示中の patch の base が、remote に無い commit だったときの理由。ローカルの
 * base branch が push 前の commit を持っていると、その分が patch から落ちる。 */
export const MERGE_DIFF_LOCAL_BASE = msg`表示中の差分は未 push の base commit を基準にしています。base branch を push してから実行してください`;

export const MERGE_STALE_DIFF = msg`表示中の差分より PR が進んでいます。再取得してから実行してください`;

export const MERGE_METHOD_LABELS: Record<MergeMethod, MessageDescriptor> = {
  squash: msg`squash — 1 コミットに潰す`,
  merge: msg`merge commit — マージコミットを作る`,
  rebase: msg`rebase — base に付け替える`,
};
