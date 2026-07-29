import { useCallback, useEffect, useState } from "react";
import { diffErrorMessage } from "../lib/diff";
import type { DiffResponse } from "../lib/types";

export type DiffState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; diff: DiffResponse };

/* オーバーレイを開いた時に 1 回だけ取得する。サーバー側は 1 request に共有
 * 10s deadline を持つ重い読み出しなのでポーリングせず、更新は明示的な
 * refetch のみ。url は呼び出し側が apiUrl で組んだ完成形(値で安定)を受け、
 * unmount / url 変更時は in-flight リクエストを abort する。 */
export function useDiff(url: string): { state: DiffState; refetch: () => void } {
  const [state, setState] = useState<DiffState>({ phase: "loading" });
  const [epoch, setEpoch] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    setState({ phase: "loading" });

    const fetchDiff = async () => {
      try {
        const res = await fetch(url, { cache: "no-store", signal: ctrl.signal });
        if (!res.ok) {
          setState({ phase: "error", message: await diffErrorMessage(res) });
          return;
        }
        const diff = (await res.json()) as DiffResponse;
        if (ctrl.signal.aborted) return;
        setState({ phase: "ready", diff });
      } catch (err) {
        if (ctrl.signal.aborted || (err instanceof DOMException && err.name === "AbortError")) {
          return;
        }
        setState({ phase: "error", message: "diff の取得に失敗しました(接続エラー)" });
      }
    };

    void fetchDiff();
    return () => ctrl.abort();
  }, [url, epoch]);

  const refetch = useCallback(() => setEpoch((n) => n + 1), []);
  return { state, refetch };
}
