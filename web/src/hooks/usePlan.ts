import { useCallback, useEffect, useState } from "react";
import { apiUrl } from "../lib/api";
import { clock } from "../lib/format";
import type { PlanResponse } from "../lib/types";

export interface PlanView {
  text: string;
  meta: string;
  refetch: () => void;
}

const UNAVAILABLE = "(plan を取得できませんでした)";
const NOT_FOUND =
  "(plan が見つかりません — まだ提案されていないか、画面外へスクロールアウトしています)";

/* usePeek 踏襲の /api/plan 取得 hook。ただしポーリングはしない: plan は peek の
 * ような流れる出力ではなく一度提案されたら安定する文書なので、mount 時に一度
 * fetch + ユーザーの「再取得」での手動 refetch のみ。capture 由来のテキストは
 * 敵性入力 — 呼び出し側はテキストノード以外で描画しないこと(markdown レンダ禁止)。
 * ペイン切替・unmount 時は effect cleanup が in-flight リクエストを abort する。 */
export function usePlan(pane: { paneId: string; alive: boolean } | null, token: string): PlanView {
  const [view, setView] = useState<Omit<PlanView, "refetch">>({ text: UNAVAILABLE, meta: "—" });
  const [generation, setGeneration] = useState(0);
  const refetch = useCallback(() => setGeneration((g) => g + 1), []);
  const paneId = pane?.paneId ?? null;
  const alive = pane?.alive ?? false;

  useEffect(() => {
    if (!paneId) return;
    if (!alive) {
      setView({ text: "(plan を取得できません — ペインは終了しています)", meta: "—" });
      return;
    }

    let disposed = false;
    const ctrl = new AbortController();

    const fetchPlan = async () => {
      try {
        const res = await fetch(apiUrl("/api/plan", token, { pane: paneId }), {
          cache: "no-store",
          signal: ctrl.signal,
        });
        if (disposed) return;
        if (!res.ok) {
          setView({ text: UNAVAILABLE, meta: "—" });
          return;
        }
        const body = (await res.json()) as PlanResponse;
        if (disposed) return;
        setView({
          text: body.found ? body.plan : NOT_FOUND,
          meta: `captured ${clock(body.capturedAt)}`,
        });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setView({ text: UNAVAILABLE, meta: "—" });
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
