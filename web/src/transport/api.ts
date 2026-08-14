/* API URL 組み立て。token(サーバが要求する場合のみ)はこのページ自身の URL の
 * ?token= から読み、query param として全 API 呼び出しに付与する(EventSource は
 * ヘッダを送れないため query param 一択)。index.html の
 * <meta name="referrer" content="no-referrer"> が外部漏洩を防ぐ。 */

export function readToken(search: string = location.search): string {
  return new URLSearchParams(search).get("token") ?? "";
}

export function apiUrl(path: string, token: string, params?: Record<string, string>): string {
  const sp = new URLSearchParams(params);
  if (token) sp.set("token", token);
  const qs = sp.toString();
  return qs ? `${path}?${qs}` : path;
}

/* mutation は JSON 固定。application/json は CORS の単純リクエストではないので、
 * 別オリジンからの fetch は preflight され、mux は OPTIONS を持たない — ブラウザは
 * POST 本体を送らない。<form> は そもそもこの Content-Type を付けられない。
 * これが CSRF の主防御で、サーバ側も同じ値を検証する(dashboard/server.go の
 * sameOriginOnly)。 */
export function postJson(
  path: string,
  token: string,
  init: { params?: Record<string, string>; body: unknown; signal?: AbortSignal },
): Promise<Response> {
  return fetch(apiUrl(path, token, init.params), {
    method: "POST",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(init.body),
    signal: init.signal,
  });
}

/* エラー応答の本文。サーバは固定文言を {"error"}、可変の理由(gh の stderr など)を
 * {"detail"}、機械可読な分類を {"code"} に入れる。detail を優先するのは、branch
 * protection や無効な merge 方式の具体的な理由がそこにしか無いため。JSON でない
 * body(middleware の text/plain)は全て空文字になる。 */
export async function errorBody(res: Response): Promise<{ detail: string; code: string }> {
  try {
    const body: unknown = await res.json();
    if (body && typeof body === "object") {
      const o = body as { error?: unknown; detail?: unknown; code?: unknown };
      return {
        detail: String(o.detail ?? "") || String(o.error ?? ""),
        code: String(o.code ?? ""),
      };
    }
  } catch {
    /* 本文が JSON でない/空。呼び出し側はステータスだけで文言を決める。 */
  }
  return { detail: "", code: "" };
}
