import { useMemo, type CSSProperties } from "react";
import { filterTokens, tokenForKey } from "../lib/filter";
import { FilterDropdown, type Option } from "./FilterDropdown";

/* 静的キーのドロップダウン定義。options をモジュール定数にすることで
 * memo(FilterDropdown) が snapshot tick の再レンダーをスキップできる。 */
const STATIC_DROPDOWNS: readonly { key: string; ariaLabel: string; options: readonly Option[] }[] =
  [
    {
      key: "state",
      ariaLabel: "issue / tmux 状態で絞り込み",
      options: [
        ["open", "open"],
        ["closed", "closed"],
        ["live", "live"],
        ["stale", "stale"],
        ["queued", "queued"], // 未開始(synthetic)行の tmux 状態
        ["deferred", "deferred"], // blocker 待ちの未開始行
      ],
    },
    {
      key: "run",
      ariaLabel: "agent 実行状態で絞り込み",
      options: [
        ["running", "running"],
        ["working", "working"],
        ["idle", "idle"],
        ["plan", "plan"],
        ["blocked", "blocked"],
        ["done", "done"],
      ],
    },
    {
      key: "ci",
      ariaLabel: "CI 結果で絞り込み",
      options: [
        ["pass", "pass"],
        ["fail", "fail"],
        ["pending", "pending"],
      ],
    },
    {
      key: "dirty",
      ariaLabel: "worktree の dirty 状態で絞り込み",
      options: [
        ["yes", "yes"],
        ["no", "no"],
      ],
    },
    {
      key: "live",
      ariaLabel: "tmux ペイン生死で絞り込み",
      options: [
        ["yes", "yes"],
        ["no", "no"],
      ],
    },
    {
      key: "pr",
      ariaLabel: "PR 状態で絞り込み",
      options: [
        ["merged", "merged"],
        ["open", "open"],
        ["closed", "closed"],
        ["none", "none"],
      ],
    },
  ];

export function FilterBar({
  filter,
  agents,
  waves,
  onPickToken,
  onClearKey,
  onRemoveToken,
}: {
  filter: string;
  agents: string[];
  waves: number[];
  onPickToken: (key: string, value: string) => void;
  onClearKey: (key: string) => void;
  onRemoveToken: (tok: string) => void;
}) {
  const tokens = filterTokens(filter);
  const agentOptions = useMemo(() => agents.map((a): Option => [a, a]), [agents]);
  const waveOptions = useMemo(() => waves.map((w): Option => [String(w), `w${w}`]), [waves]);
  const dd = (key: string) => ({
    dataKey: key,
    active: tokenForKey(tokens, key),
    onPickToken,
    onClearKey,
  });
  return (
    <div className="filter-bar rise" style={{ "--d": ".25s" } as CSSProperties} id="filter-bar">
      {STATIC_DROPDOWNS.map((d) => (
        <FilterDropdown key={d.key} {...dd(d.key)} ariaLabel={d.ariaLabel} options={d.options} />
      ))}
      <FilterDropdown
        {...dd("agent")}
        ariaLabel="agent で絞り込み"
        options={agentOptions}
        searchable
      />
      <FilterDropdown
        {...dd("wave")}
        ariaLabel="wave で絞り込み"
        options={waveOptions}
        searchable
      />
      <span id="chips" role="list" aria-label="適用中のフィルタ">
        {tokens.map((t) => (
          <button
            key={t}
            type="button"
            className="chip"
            role="listitem"
            aria-label={`フィルタ ${t} を外す`}
            onClick={() => onRemoveToken(t)}
          >
            {t}
            <span className="x" aria-hidden="true">
              ×
            </span>
          </button>
        ))}
      </span>
    </div>
  );
}
