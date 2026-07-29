import { useCallback, useEffect, useRef, useState } from "react";
import { apiUrl } from "../lib/api";
import type { DiffResponse } from "../lib/types";

export type DiffState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; diff: DiffResponse };

/* /api/diff のエラー body は {"error":"message"}(text は敵性入力 — 呼び出し側は
 * テキストノードのみで描画)。token/405 の middleware エラーは text/plain。 */
async function errorMessage(res: Response): Promise<string> {
  let detail = "";
  try {
    const body: unknown = await res.json();
    if (body && typeof body === "object" && "error" in body) {
      detail = String((body as { error: unknown }).error);
    }
  } catch {
    /* JSON でない body(middleware の text/plain 等)は詳細なし */
  }
  const head =
    res.status === 404
      ? "diff を取得できません — worktree の記録が見つかりません(cleanup 済みか、サーバーが /api/diff 未対応の可能性)"
      : res.status === 502
        ? "サーバーが diff を安全に生成できませんでした"
        : `diff の取得に失敗しました (HTTP ${res.status})`;
  return detail ? `${head}: ${detail}` : head;
}

/* オーバーレイを開いた時に 1 回だけ取得する。サーバー側は 1 request に共有
 * 10s deadline を持つ重い読み出しなのでポーリングせず、更新は明示的な
 * refetch のみ。unmount / query 変更時は in-flight リクエストを abort する。 */
export function useDiff(
  query: Record<string, string>,
  token: string,
): { state: DiffState; refetch: () => void } {
  const [state, setState] = useState<DiffState>({ phase: "loading" });
  const [epoch, setEpoch] = useState(0);
  const ctrlRef = useRef<AbortController | null>(null);
  /* Record は呼び出し側の再レンダーで参照が変わるため、値で比較する */
  const queryKey = JSON.stringify(query);

  useEffect(() => {
    const params = JSON.parse(queryKey) as Record<string, string>;
    let disposed = false;
    const ctrl = new AbortController();
    ctrlRef.current = ctrl;
    setState({ phase: "loading" });

    const fetchDiff = async () => {
      try {
        const res = await fetch(apiUrl("/api/diff", token, params), {
          cache: "no-store",
          signal: ctrl.signal,
        });
        if (disposed) return;
        if (!res.ok) {
          setState({ phase: "error", message: await errorMessage(res) });
          return;
        }
        const diff = (await res.json()) as DiffResponse;
        if (disposed) return;
        setState({ phase: "ready", diff });
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) return;
        setState({ phase: "error", message: "diff の取得に失敗しました(接続エラー)" });
      }
    };

    void fetchDiff();
    return () => {
      disposed = true;
      ctrl.abort();
    };
  }, [queryKey, token, epoch]);

  const refetch = useCallback(() => setEpoch((n) => n + 1), []);
  return { state, refetch };
}
