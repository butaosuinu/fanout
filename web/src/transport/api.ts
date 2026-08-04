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
