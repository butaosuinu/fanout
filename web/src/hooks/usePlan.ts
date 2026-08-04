import { useLingui } from "@lingui/react/macro";
import { useCallback, useEffect, useState } from "react";
import { apiUrl } from "../lib/api";
import { clock } from "../lib/format";
import type { PlanResponse } from "../lib/types";

export interface PlanView {
  text: string;
  meta: string;
  found: boolean;
  loading: boolean;
  refetch: () => void;
}

/* usePeek と同じ理由で、state には生データだけを置いて文言は返す直前に組む
 * (翻訳済み文字列を溜めると、言語切替のあと再取得するまで古い言語が残る)。
 * plan が見つかったときだけ text は capture 由来の実文書になる。 */
type PlanState = {
  /* 実際に取得できた plan 本文。プレースホルダは placeholder 側で決める。 */
  plan: string | null;
  placeholder: "loading" | "unavailable" | "notFound" | "ended";
  capturedAt: string | null;
  found: boolean;
  loading: boolean;
};

const INITIAL: PlanState = {
  plan: null,
  placeholder: "loading",
  capturedAt: null,
  found: false,
  loading: true,
};

/* usePeek 踏襲の /api/plan 取得 hook。ただしポーリングはしない: plan は peek の
 * ような流れる出力ではなく一度提案されたら安定する文書なので、mount 時に一度
 * fetch + ユーザーの「再取得」での手動 refetch のみ。capture 由来のテキストは
 * 敵性入力 — 呼び出し側はテキストノード以外で描画しないこと(markdown レンダ禁止)。
 * ペイン切替・unmount 時は effect cleanup が in-flight リクエストを abort する。
 * 一度表示できた plan は alive が落ちても保持する(pane 終了で読みかけの文書を
 * 消さない)。 */
export function usePlan(pane: { paneId: string; alive: boolean } | null, token: string): PlanView {
  const { t } = useLingui();
  const [state, setState] = useState<PlanState>(INITIAL);
  const [generation, setGeneration] = useState(0);
  const refetch = useCallback(() => setGeneration((g) => g + 1), []);
  const paneId = pane?.paneId ?? null;
  const alive = pane?.alive ?? false;

  useEffect(() => {
    if (!paneId) return;
    if (!alive) {
      // 取得済みの plan は残す(snapshot の alive 反転で文書を吹き飛ばさない)
      setState((v) =>
        v.found
          ? v
          : { plan: null, placeholder: "ended", capturedAt: null, found: false, loading: false },
      );
      return;
    }

    let disposed = false;
    const ctrl = new AbortController();
    setState((v) => ({ ...v, loading: true, ...(v.found ? {} : { placeholder: "loading" }) }));

    const fetchPlan = async () => {
      try {
        const res = await fetch(apiUrl("/api/plan", token, { pane: paneId }), {
          cache: "no-store",
          signal: ctrl.signal,
        });
        if (disposed) return;
        if (!res.ok) {
          setState({
            plan: null,
            placeholder: "unavailable",
            capturedAt: null,
            found: false,
            loading: false,
          });
          return;
        }
        const body = (await res.json()) as PlanResponse;
        if (disposed) return;
        setState({
          plan: body.found ? (body.plan ?? "") : null,
          placeholder: "notFound",
          capturedAt: body.capturedAt,
          found: body.found,
          loading: false,
        });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setState({
          plan: null,
          placeholder: "unavailable",
          capturedAt: null,
          found: false,
          loading: false,
        });
      }
    };

    void fetchPlan();
    return () => {
      disposed = true;
      ctrl.abort();
    };
  }, [paneId, alive, token, generation]);

  const placeholders = {
    loading: t`(plan を取得中…)`,
    unavailable: t`(plan を取得できませんでした)`,
    notFound: t`(plan が見つかりません — まだ提案されていないか、画面外へスクロールアウトしています)`,
    ended: t`(plan を取得できません — ペインは終了しています)`,
  };
  return {
    text: state.plan ?? placeholders[state.placeholder],
    meta: state.capturedAt ? `captured ${clock(state.capturedAt)}` : "—",
    found: state.found,
    loading: state.loading,
    refetch,
  };
}
