import type { MessageDescriptor } from "@lingui/core";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  MERGE_NETWORK_ERROR,
  MERGE_PATH,
  mergeErrorMessage,
  mergeRequestBody,
  type MergeRequest,
} from "../features/merge/merge";
import { postJson } from "./api";

export type MergeState =
  | { phase: "idle" }
  | { phase: "sending" }
  /* message は文字列ではなく descriptor。送信時ではなく描画時に解決させることで、
   * エラー表示中に言語を切り替えても文言が取り残されない(useDiff と同じ理由)。 */
  | { phase: "error"; message: MessageDescriptor };

/* 送信 1 回の結果。abort は "aborted"(unmount 済みなので何も表示しない)。 */
type PostOutcome =
  | { kind: "ok"; queued: boolean; unknown: boolean }
  | { kind: "failed"; message: MessageDescriptor }
  | { kind: "aborted" };

export interface MergeOutcome {
  ok: boolean;
  /* GitHub が受理したがまだマージされていない(merge queue)。行はまだ merged に
   * ならないので、反映待ちにはせずその旨を伝える。 */
  queued: boolean;
  /* merge コマンドは通ったが結果を確認できなかった。再送すると不明な状態に
   * 二度目のマージを撃つことになるので、snapshot で決着するまで塞ぐ。 */
  unknown: boolean;
}

async function postMerge(
  token: string,
  req: MergeRequest,
  signal: AbortSignal,
): Promise<PostOutcome> {
  try {
    const res = await postJson(MERGE_PATH, token, {
      params: req.query,
      body: mergeRequestBody(req),
      signal,
    });
    if (!res.ok) return { kind: "failed", message: await mergeErrorMessage(res) };
    const body = (await res.json()) as { queued?: unknown; unknown?: unknown };
    return { kind: "ok", queued: body.queued === true, unknown: body.unknown === true };
  } catch (err) {
    if (signal.aborted || (err instanceof DOMException && err.name === "AbortError")) {
      return { kind: "aborted" };
    }
    return { kind: "failed", message: MERGE_NETWORK_ERROR };
  }
}

const NOT_SENT: MergeOutcome = { ok: false, queued: false, unknown: false };

function applyOutcome(
  outcome: Extract<PostOutcome, { kind: "ok" | "failed" }>,
  setState: (s: MergeState) => void,
): MergeOutcome {
  if (outcome.kind === "failed") {
    setState({ phase: "error", message: outcome.message });
    return NOT_SENT;
  }
  setState({ phase: "idle" });
  return { ok: true, queued: outcome.queued, unknown: outcome.unknown };
}

/* ダッシュボード唯一の mutation。effect 駆動の useDiff と違いユーザー起動なので、
 * in-flight の管理は ref で持つ。in-flight 中の再入は撃たない — 確認ダイアログを
 * 挟まない導線なので、二重送信の最後の砦がここ。呼び出し側が「撃たれなかった」を
 * 事前に知れるよう busy() も出す(結果の帰属先を進めてしまわないため)。 */
export function useMergePr(token: string): {
  state: MergeState;
  busy: () => boolean;
  submit: (req: MergeRequest) => Promise<MergeOutcome>;
} {
  const [state, setState] = useState<MergeState>({ phase: "idle" });
  const inFlight = useRef<AbortController | null>(null);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      inFlight.current?.abort();
    };
  }, []);

  const busy = useCallback(() => inFlight.current !== null, []);

  const submit = useCallback(
    async (req: MergeRequest): Promise<MergeOutcome> => {
      if (inFlight.current) return NOT_SENT;
      inFlight.current = new AbortController();
      setState({ phase: "sending" });
      const outcome = await postMerge(token, req, inFlight.current.signal);
      inFlight.current = null;
      if (!alive.current || outcome.kind === "aborted") return NOT_SENT;
      return applyOutcome(outcome, setState);
    },
    [token],
  );

  return { state, busy, submit };
}
