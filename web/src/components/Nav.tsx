import { useTheme } from "../hooks/useTheme";
import type { ConnState } from "../hooks/useSnapshot";

function ThemeToggle() {
  const { theme, toggle } = useTheme();
  return (
    <button
      id="theme-toggle"
      className="theme-toggle"
      type="button"
      aria-label="ライト / ダーク切替"
      aria-pressed={theme === "dark"}
      onClick={toggle}
    >
      <svg
        className="ic-moon"
        width="15"
        height="15"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        aria-hidden="true"
      >
        <path d="M20.4 14.2A8.6 8.6 0 0 1 9.8 3.6a8.6 8.6 0 1 0 10.6 10.6Z" />
      </svg>
      <svg
        className="ic-sun"
        width="15"
        height="15"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="round"
        aria-hidden="true"
      >
        <circle cx="12" cy="12" r="4.2" />
        <path d="M12 2.5v2.4M12 19.1v2.4M2.5 12h2.4M19.1 12h2.4M5.2 5.2l1.7 1.7M17.1 17.1l1.7 1.7M18.8 5.2l-1.7 1.7M6.9 17.1l-1.7 1.7" />
      </svg>
    </button>
  );
}

export function Nav({
  repo,
  projectRoot,
  conn,
}: {
  repo: string;
  projectRoot: string;
  conn: ConnState;
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
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}
