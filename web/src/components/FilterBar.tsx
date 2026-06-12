import { type CSSProperties } from "react";
import { filterTokens, tokenForKey } from "../lib/filter";
import { FilterDropdown, type Option } from "./FilterDropdown";

export function FilterBar({
  filter,
  agents,
  waves,
  onPickToken,
  onRemoveToken,
}: {
  filter: string;
  agents: string[];
  waves: number[];
  onPickToken: (key: string, value: string) => void;
  onRemoveToken: (tok: string) => void;
}) {
  const tokens = filterTokens(filter);
  // 8 キー共通の props 束ね。placeholder は trigger 表示 = dataKey で統一。
  const dd = (dataKey: string) => ({
    dataKey,
    placeholder: dataKey,
    active: tokenForKey(tokens, dataKey),
    onPickToken,
    onRemoveToken,
  });
  return (
    <div className="filter-bar rise" style={{ "--d": ".25s" } as CSSProperties} id="filter-bar">
      <FilterDropdown
        {...dd("state")}
        ariaLabel="issue / tmux 状態で絞り込み"
        options={[
          ["open", "open"],
          ["closed", "closed"],
          ["live", "live"],
          ["stale", "stale"],
        ]}
      />
      <FilterDropdown
        {...dd("run")}
        ariaLabel="agent 実行状態で絞り込み"
        options={[
          ["running", "running"],
          ["done", "done"],
        ]}
      />
      <FilterDropdown
        {...dd("ci")}
        ariaLabel="CI 結果で絞り込み"
        options={[
          ["pass", "pass"],
          ["fail", "fail"],
          ["pending", "pending"],
        ]}
      />
      <FilterDropdown
        {...dd("dirty")}
        ariaLabel="worktree の dirty 状態で絞り込み"
        options={[
          ["yes", "yes"],
          ["no", "no"],
        ]}
      />
      <FilterDropdown
        {...dd("live")}
        ariaLabel="tmux ペイン生死で絞り込み"
        options={[
          ["yes", "yes"],
          ["no", "no"],
        ]}
      />
      <FilterDropdown
        {...dd("pr")}
        ariaLabel="PR 状態で絞り込み"
        options={[
          ["merged", "merged"],
          ["open", "open"],
          ["closed", "closed"],
          ["none", "none"],
        ]}
      />
      <FilterDropdown
        {...dd("agent")}
        ariaLabel="agent で絞り込み"
        options={agents.map((a): Option => [a, a])}
        searchable
      />
      <FilterDropdown
        {...dd("wave")}
        ariaLabel="wave で絞り込み"
        options={waves.map((w): Option => [String(w), `w${w}`])}
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
