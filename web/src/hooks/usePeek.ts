import { useEffect, useState } from "react";
import { apiUrl } from "../lib/api";
import { clock } from "../lib/format";
import type { PeekResponse } from "../lib/types";

export interface PeekView {
  output: string;
  meta: string;
}

const PEEK_INTERVAL_MS = 5000;
const UNAVAILABLE: PeekView = { output: "(pane output unavailable)", meta: "—" };

/* ドロワーを開いている間、生きているペインだけを 5s ごとに /api/peek へ。
 * capture 出力は敵性入力 — 呼び出し側はテキストノード以外で描画しないこと。
 * ペイン切替・unmount 時は effect cleanup が in-flight リクエストを abort し、
 * 古いペインの応答が一瞬表示されるのを防ぐ。 */
export function usePeek(pane: { paneId: string; alive: boolean } | null, token: string): PeekView {
  const [view, setView] = useState<PeekView>(UNAVAILABLE);
  const paneId = pane?.paneId ?? null;
  const alive = pane?.alive ?? false;

  useEffect(() => {
    if (!paneId) return;
    if (!alive) {
      setView({ output: "(pane output unavailable — ペインは終了しています)", meta: "—" });
      return;
    }

    let disposed = false;
    let ctrl: AbortController | null = null;

    const fetchPeek = async () => {
      ctrl?.abort();
      ctrl = new AbortController();
      try {
        const res = await fetch(apiUrl("/api/peek", token, { pane: paneId }), {
          cache: "no-store",
          signal: ctrl.signal,
        });
        if (disposed) return;
        if (!res.ok) {
          setView(UNAVAILABLE);
          return;
        }
        const body = (await res.json()) as PeekResponse;
        if (disposed) return;
        setView({
          output: body.output ?? "",
          meta: `captured ${clock(body.capturedAt)} · ${body.lines} lines · 5s ごとに更新`,
        });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setView(UNAVAILABLE);
      }
    };

    void fetchPeek();
    const timer = setInterval(() => void fetchPeek(), PEEK_INTERVAL_MS);
    return () => {
      disposed = true;
      clearInterval(timer);
      ctrl?.abort();
    };
  }, [paneId, alive, token]);

  return view;
}
