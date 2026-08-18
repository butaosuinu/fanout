import { useCallback, useEffect, useState } from "react";
import type { PRRef, Snapshot } from "../../transport/types";
import type { MergeOutcome } from "../../transport/useMergePr";
import { findPaneEntry } from "../sessions/pane";

/* 送信済みだが snapshot がまだ追いつかない間、ボタンを押せなくしておく上限。
 * サーバは merge 成功時に GitHub tick を 1 回前倒しするので通常は数秒で解ける。
 * gh tick 6 回ぶんを過ぎても MERGED にならないなら、こちらの想定外(webhook 事故、
 * 別経路での revert)なので通常判定へ戻す — 永久に押せないほうが困る。 */
const PENDING_BACKSTOP_MS = 120_000;

/* 送信した行・PR・時刻。PR 番号まで持つのは、同じ行に複数 PR があるとき「今
 * マージしたやつ」が反映されたかだけを見るため。
 *
 * 単数ではなく集合で持つ: PR を続けて操作すると、後の送信が前の hold を上書きし、
 * まだ決着していない PR のボタンが押せる状態に戻ってしまう(サーバは claim と
 * live fence で必ず 409 を返すので、押せるのに必ず失敗するボタンになる)。 */
export type Pending = { key: string; prNumber: number; repo: string; since: number };

/* 決着待ちのマージ。行キーではなく PR 番号 + repository で持つ — 同じ PR が複数行に
 * 載る場合(複数 issue を close する PR)に、別の行から再送できてしまうのを防ぐ。
 *
 * queued(merge queue が受理した)は armed も持つ。auto-merge の取り消しは PR を
 * OPEN のまま残すので、merged / closed だけを解除条件にすると hold が永久に残る。
 * 結果不明の側はこの出口を使わない — 既にマージされている可能性があるため。 */
export type Unknown = {
  prNumber: number;
  repo: string;
  queued?: boolean;
  /* poll が「GitHub がまだ保留している」ことを実際に見た。マージ前の snapshot は
   * まだ保留を持たないので、その不在を取り消しと読むと hold を取った直後に
   * 落としてしまう。 */
  seen?: boolean;
};

/* 反映されたら pending を解除する。楽観更新はしない — snapshot を書き換えると、
 * サーバ側で失敗したマージまで成功したように見せてしまう。 */
export function usePendingRelease(
  snap: Snapshot | null,
  pending: Pending[],
  setPending: (next: (prev: Pending[]) => Pending[]) => void,
) {
  useEffect(() => {
    if (!snap || !pending.length) return;
    const live = pending.filter((p) => !pendingResolved(snap, p));
    if (live.length !== pending.length) setPending(() => live);
  }, [snap, pending, setPending]);

  /* backstop は実時間で切る。上の effect は snapshot が変わったときにしか走らず、
   * SSE は内容が変わらない限り配信しないので、merge 後の GitHub refresh が失敗して
   * 内容が動かないと 120 秒を過ぎても再評価されず、ボタンが永久に「反映待ち」で
   * 固まる。 */
  const oldest = pending.length ? Math.min(...pending.map((p) => p.since)) : 0;
  useEffect(() => {
    if (!oldest) return;
    const left = Math.max(0, oldest + PENDING_BACKSTOP_MS - Date.now());
    const timer = setTimeout(() => setPending(dropExpired), left);
    return () => clearTimeout(timer);
  }, [oldest, setPending]);
}

/* backstop を過ぎた反映待ちを落とす。 */
function dropExpired(pending: Pending[]): Pending[] {
  const cutoff = Date.now() - PENDING_BACKSTOP_MS;
  return pending.filter((p) => p.since > cutoff);
}

/* 見るのは「今マージした PR 番号」だけ。行の primary PR を見ると、同じ行に別の
 * open PR が残っている間ずっと解除されない。行ごと消えた場合も解除する。 */
function pendingResolved(snap: Snapshot, pending: Pending): boolean {
  const entry = findPaneEntry(snap, pending.key);
  if (!entry) return true;
  /* repository も見る: 同じ行に別 repository の同番号 PR が載ると、そちらの状態で
   * 反映済みと判定してしまう(サーバの prSettled と同じ規則)。 */
  const repo = pending.repo.toLowerCase();
  const pr = entry.pane.prs?.find(
    (p) => p.number === pending.prNumber && p.baseRepo?.toLowerCase() === repo,
  );
  return !!pr && (pr.state === "MERGED" || !!pr.mergedAt);
}

/* 結果不明は GitHub の新しい状態でだけ決着する。時間では解除しない — 未確定の
 * まま撃ち直させないことが目的なので、時間切れで再送を許すと意味がなくなる。 */
export function useUnknownRelease(
  snap: Snapshot | null,
  unknown: Unknown[],
  setUnknown: (next: (prev: Unknown[]) => Unknown[]) => void,
) {
  useEffect(() => {
    if (!snap || !unknown.length) return;
    const next = advanceHolds(snap, unknown);
    if (changed(unknown, next)) setUnknown(() => next);
  }, [snap, unknown, setUnknown]);
}

/* 観測を書き込んでから、終わったものを外す。順序に意味がある — armed を見た
 * 直後の要素は取り消しではない。サーバの claimOver と同じ規則。 */
function advanceHolds(snap: Snapshot, holds: Unknown[]): Unknown[] {
  return holds.map((held) => armedSeen(snap, held)).filter((held) => !holdOver(snap, held));
}

function armedSeen(snap: Snapshot, held: Unknown): Unknown {
  if (!held.queued || held.seen) return held;
  return mergePending(findPR(snap, held)) ? { ...held, seen: true } : held;
}

/* GitHub がまだこのマージを保留しているか。gh は checks 未完了なら auto-merge を
 * 武装し、完了済みなら直接 queue に入れるので、両方を見る。 */
function mergePending(pr: PRRef | undefined): boolean {
  return !!pr && (!!pr.autoMerge || !!pr.queued);
}

/* 取り消しは true -> false の遷移だけ。観測前の false はマージ前の snapshot。 */
function holdOver(snap: Snapshot, held: Unknown): boolean {
  if (settledPR(snap, held)) return true;
  if (!held.queued || !held.seen) return false;
  const pr = findPR(snap, held);
  return !!pr && !mergePending(pr);
}

function changed(prev: Unknown[], next: Unknown[]): boolean {
  return prev.length !== next.length || next.some((u, i) => u !== prev[i]);
}

/* repository も見る: `Fixes owner/repo#N` は別 repository の PR を行に載せるし、
 * PR 番号は repository ごとに重複する。よその merged #7 でこちらの #7 の hold を
 * 解いてしまうと、結果不明のマージを撃ち直せる(サーバの prSettled と同じ規則)。 */
function settledPR(snap: Snapshot, held: { prNumber: number; repo: string }): boolean {
  return refsOf(snap, held).some(
    (pr) => pr.state === "MERGED" || pr.state === "CLOSED" || !!pr.mergedAt,
  );
}

function findPR(snap: Snapshot, held: { prNumber: number; repo: string }): PRRef | undefined {
  return refsOf(snap, held)[0];
}

function refsOf(snap: Snapshot, held: { prNumber: number; repo: string }): PRRef[] {
  const repo = held.repo.toLowerCase();
  return (snap.sessions ?? [])
    .flatMap((s) => s.panes ?? [])
    .flatMap((p) => p.prs ?? [])
    .filter((pr) => pr.number === held.prNumber && pr.baseRepo?.toLowerCase() === repo);
}

/* 直近の送信の追跡先。行キー(pending)と PR 番号(unknown)で粒度が違うのは、
 * 反映待ちは「その行の表示が追いつくまで」だが、結果不明は「その PR が決着する
 * まで」だから — 同じ PR が複数行に載る場合、別の行から撃ち直させない。 */
export type Notice = { key: string; kind: "queued" | "unknown" } | null;

/* 送信した行の識別子。repo は hold の解除条件に要る(番号は repository ごとに
 * 重複する)。 */
export type Row = { key: string; prNumber: number; repo: string };

export interface MergeTracking {
  lastKey: string | null;
  pending: Pending[];
  unknown: Unknown[];
  notice: Notice;
  /* 送信開始。結果の帰属先をこの行へ移す。 */
  begin: (key: string) => void;
  apply: (row: Row, res: MergeOutcome) => void;
}

/* 送信結果の追跡をまとめて持つ。unknown も queued も、決着するまでその PR の
 * ボタンを塞ぐ。queued(merge queue が受理した)は既に auto-merge が武装済みで、
 * サーバも同じ理由で claim に載せるので、押せるままにしても 409 が返るだけ —
 * しかも受理を伝えた notice がクリックで消える。解除条件はサーバの claim と同じで、
 * その PR が merged か closed になったとき。 */
export function useMergeTracking(snap: Snapshot | null): MergeTracking {
  const [lastKey, setLastKey] = useState<string | null>(null);
  const [pending, setPending] = useState<Pending[]>([]);
  const [unknown, setUnknown] = useState<Unknown[]>([]);
  const [notice, setNotice] = useState<Notice>(null);

  usePendingRelease(snap, pending, setPending);
  useUnknownRelease(snap, unknown, setUnknown);

  const begin = useCallback((key: string) => {
    setLastKey(key);
    setNotice(null);
  }, []);

  const apply = useCallback((row: Row, res: MergeOutcome) => {
    /* 追加であって置き換えではない。前の送信の hold を落とすと、まだ決着して
     * いない PR のボタンが押せる状態に戻る。 */
    if (res.unknown || res.queued) {
      setUnknown((prev) => [
        ...prev,
        { prNumber: row.prNumber, repo: row.repo, queued: res.queued },
      ]);
    } else {
      setPending((prev) => [
        ...prev,
        { key: row.key, prNumber: row.prNumber, repo: row.repo, since: Date.now() },
      ]);
    }
    setNotice(noticeFor(row.key, res));
  }, []);

  return { lastKey, pending, unknown, notice, begin, apply };
}

function noticeFor(key: string, res: MergeOutcome): Notice {
  if (res.unknown) return { key, kind: "unknown" };
  if (res.queued) return { key, kind: "queued" };
  return null;
}
