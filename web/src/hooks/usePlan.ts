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

const LOADING = "(plan を取得中…)";
const UNAVAILABLE = "(plan を取得できませんでした)";
const NOT_FOUND =
  "(plan が見つかりません — まだ提案されていないか、画面外へスクロールアウトしています)";

type ViewState = Omit<PlanView, "refetch">;

/* usePeek 踏襲の /api/plan 取得 hook。ただしポーリングはしない: plan は peek の
 * ような流れる出力ではなく一度提案されたら安定する文書なので、mount 時に一度
 * fetch + ユーザーの「再取得」での手動 refetch のみ。capture 由来のテキストは
 * 敵性入力 — 呼び出し側はテキストノード以外で描画しないこと(markdown レンダ禁止)。
 * ペイン切替・unmount 時は effect cleanup が in-flight リクエストを abort する。
 * 一度表示できた plan は alive が落ちても保持する(pane 終了で読みかけの文書を
 * 消さない)。 */
export function usePlan(pane: { paneId: string; alive: boolean } | null, token: string): PlanView {
  const [view, setView] = useState<ViewState>({ text: LOADING, meta: "—", found: false, loading: true });
  const [generation, setGeneration] = useState(0);
  const refetch = useCallback(() => setGeneration((g) => g + 1), []);
  const paneId = pane?.paneId ?? null;
  const alive = pane?.alive ?? false;

  useEffect(() => {
    if (!paneId) return;
    if (!alive) {
      // 取得済みの plan は残す(snapshot の alive 反転で文書を吹き飛ばさない)
      setView((v) =>
        v.found ? v : { text: "(plan を取得できません — ペインは終了しています)", meta: "—", found: false, loading: false },
      );
      return;
    }

    let disposed = false;
    const ctrl = new AbortController();
    setView((v) => ({ ...v, loading: true, ...(v.found ? {} : { text: LOADING }) }));

    const fetchPlan = async () => {
      try {
        const res = await fetch(apiUrl("/api/plan", token, { pane: paneId }), {
          cache: "no-store",
          signal: ctrl.signal,
        });
        if (disposed) return;
        if (!res.ok) {
          setView({ text: UNAVAILABLE, meta: "—", found: false, loading: false });
          return;
        }
        const body = (await res.json()) as PlanResponse;
        if (disposed) return;
        setView({
          text: body.found ? (body.plan ?? "") : NOT_FOUND,
          meta: `captured ${clock(body.capturedAt)}`,
          found: body.found,
          loading: false,
        });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setView({ text: UNAVAILABLE, meta: "—", found: false, loading: false });
      }
    };

    void fetchPlan();
    return () => {
      disposed = true;
      ctrl.abort();
    };
  }, [paneId, alive, token, generation]);

  return { ...view, refetch };
}
