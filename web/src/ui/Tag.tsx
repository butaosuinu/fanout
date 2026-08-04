import type { ReactNode } from "react";

export function Tag({
  cls = "",
  title,
  children,
}: {
  cls?: string;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span className={cls ? `tag ${cls}` : "tag"} title={title}>
      {children}
    </span>
  );
}

/* url が空(repo 未解決・@manual の負番号 issue など)ならリンク化せず子を
 * そのまま返す。url の安全性は lib/github の検証で担保済み — ここでは新しい
 * URL を組み立てないこと。 */
export function GhLink({
  url,
  cls = "gh",
  children,
}: {
  url: string;
  cls?: string;
  children: ReactNode;
}) {
  if (!url) return <>{children}</>;
  return (
    <a className={cls} href={url} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  );
}
