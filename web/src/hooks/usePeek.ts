import { useLingui } from "@lingui/react/macro";
import { useEffect, useState } from "react";
import { apiUrl } from "../lib/api";
import { clock } from "../lib/format";
import type { PeekResponse } from "../lib/types";

export interface PeekView {
  output: string;
  meta: string;
}

const PEEK_INTERVAL_MS = 5000;

/* state には生データだけを置き、文言は返す直前に組む。翻訳済み文字列を state に
 * 溜めると、取得の合間に言語を切り替えた分だけ古い言語が残る(peek は死んだ
 * ペインでは二度と更新されないので、そこは永久に取り残される)。 */
type PeekState =
  | { kind: "unavailable" }
  | { kind: "ended" }
  | { kind: "ok"; output: string; capturedAt: string; lines: number };

/* ドロワーを開いている間、生きているペインだけを 5s ごとに /api/peek へ。
 * capture 出力は敵性入力 — 呼び出し側はテキストノード以外で描画しないこと。
 * ペイン切替・unmount 時は effect cleanup が in-flight リクエストを abort し、
 * 古いペインの応答が一瞬表示されるのを防ぐ。 */
export function usePeek(pane: { paneId: string; alive: boolean } | null, token: string): PeekView {
  const { t } = useLingui();
  const [state, setState] = useState<PeekState>({ kind: "unavailable" });
  const paneId = pane?.paneId ?? null;
  const alive = pane?.alive ?? false;

  useEffect(() => {
    if (!paneId) return;
    if (!alive) {
      setState({ kind: "ended" });
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
          setState({ kind: "unavailable" });
          return;
        }
        const body = (await res.json()) as PeekResponse;
        if (disposed) return;
        setState({
          kind: "ok",
          output: body.output ?? "",
          capturedAt: body.capturedAt,
          lines: body.lines,
        });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setState({ kind: "unavailable" });
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

  if (state.kind === "ended") {
    return { output: t`(pane output unavailable — ペインは終了しています)`, meta: "—" };
  }
  if (state.kind === "unavailable") {
    return { output: t`(pane output unavailable)`, meta: "—" };
  }
  return {
    output: state.output,
    meta: t`captured ${{ at: clock(state.capturedAt) }} · ${{ lines: state.lines }} lines · 5s ごとに更新`,
  };
}
