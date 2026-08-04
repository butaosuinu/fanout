import { useLingui } from "@lingui/react/macro";
import type { ConnState } from "../transport/useSnapshot";

/* 外観と diff テーマは設定モーダルに集約する(将来の設定項目もそこへ足す)。
 * Nav 側はその入口 1 個だけを持つ。 */
function SettingsButton({ onClick }: { onClick: () => void }) {
  const { t } = useLingui();
  return (
    <button
      id="settings-open"
      className="settings-btn"
      type="button"
      aria-label={t`設定`}
      aria-haspopup="dialog"
      onClick={onClick}
    >
      <svg
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.7"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <circle cx="12" cy="12" r="3.1" />
        <path d="M19.4 14.5a1.6 1.6 0 0 0 .32 1.77l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.6 1.6 0 0 0-1.77-.32 1.6 1.6 0 0 0-.97 1.47V21a2 2 0 0 1-4 0v-.1a1.6 1.6 0 0 0-1.05-1.47 1.6 1.6 0 0 0-1.77.32l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.6 1.6 0 0 0 .32-1.77 1.6 1.6 0 0 0-1.47-.97H3a2 2 0 0 1 0-4h.1a1.6 1.6 0 0 0 1.47-1.05 1.6 1.6 0 0 0-.32-1.77l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.6 1.6 0 0 0 1.77.32H9a1.6 1.6 0 0 0 .97-1.47V3a2 2 0 0 1 4 0v.1a1.6 1.6 0 0 0 .97 1.47 1.6 1.6 0 0 0 1.77-.32l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.6 1.6 0 0 0-.32 1.77V9a1.6 1.6 0 0 0 1.47.97H21a2 2 0 0 1 0 4h-.1a1.6 1.6 0 0 0-1.47.97Z" />
      </svg>
    </button>
  );
}

export function Nav({
  repo,
  projectRoot,
  conn,
  onOpenSettings,
}: {
  repo: string;
  projectRoot: string;
  conn: ConnState;
  onOpenSettings: () => void;
}) {
  return (
    <header className="nav">
      <div className="nav-inner">
        <a className="brand" href="#" aria-label="fanout dashboard">
          <svg width="22" height="22" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path
              className="leaf"
              d="M1.17 14.25A12.5 12.5 0 0 1 22.83 14.25L17.2 17.5A6 6 0 0 0 6.8 17.5Z"
              strokeWidth="1.2"
              strokeLinejoin="round"
            />
            <path
              className="rib"
              d="M12 20.5 1.17 14.25M12 20.5 5.75 9.68M12 20.5 12 8M12 20.5 18.25 9.68M12 20.5 22.83 14.25"
              strokeWidth="1.5"
              strokeLinecap="round"
            />
            <circle className="pivot" cx="12" cy="20.5" r="2.2" />
          </svg>
          fanout
        </a>
        <span className="brand-sub">dashboard</span>
        <div className="nav-meta">
          <span id="repo" title={projectRoot}>
            {repo || "(repo unresolved)"}
          </span>
          <span className="conn" id="link">
            <span className={conn.up ? "pulse" : "pulse down"} aria-hidden="true"></span>
            <span className="conn-label">{conn.label}</span>
          </span>
          <SettingsButton onClick={onOpenSettings} />
        </div>
      </div>
    </header>
  );
}
