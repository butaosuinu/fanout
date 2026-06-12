import { useEffect, useRef, useState, type CSSProperties } from "react";
import { filterTokens } from "../lib/filter";

type Option = readonly [value: string, label: string];

function StaticSelect({
  dataKey,
  ariaLabel,
  placeholder,
  options,
  onPick,
}: {
  dataKey: string;
  ariaLabel: string;
  placeholder: string;
  options: Option[];
  onPick: (key: string, value: string) => void;
}) {
  return (
    <select
      data-key={dataKey}
      aria-label={ariaLabel}
      value=""
      onChange={(e) => {
        // 選択 → トークン書込 → select はプレースホルダーに戻す(常に value="")
        if (e.target.value) onPick(dataKey, e.target.value);
      }}
    >
      <option value="">{placeholder}</option>
      {options.map(([v, l]) => (
        <option key={v} value={v}>
          {l}
        </option>
      ))}
    </select>
  );
}

/* snapshot 由来の動的選択肢(agent / wave)。フォーカス中(=開いている可能性)
 * は選択肢を凍結する — 2 秒 tick がユーザーの開いたドロップダウンを閉じて
 * しまうのを防ぐ(旧 patchSelect 相当)。 */
function DynamicSelect(props: {
  id: string;
  dataKey: string;
  ariaLabel: string;
  placeholder: string;
  options: Option[];
  onPick: (key: string, value: string) => void;
}) {
  const [focused, setFocused] = useState(false);
  const frozen = useRef(props.options);
  useEffect(() => {
    if (!focused) frozen.current = props.options;
  }, [focused, props.options]);
  const options = focused ? frozen.current : props.options;
  return (
    <select
      id={props.id}
      data-key={props.dataKey}
      aria-label={props.ariaLabel}
      value=""
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      onChange={(e) => {
        if (e.target.value) props.onPick(props.dataKey, e.target.value);
      }}
    >
      <option value="">{props.placeholder}</option>
      {options.map(([v, l]) => (
        <option key={v} value={v}>
          {l}
        </option>
      ))}
    </select>
  );
}

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
  return (
    <div className="filter-bar rise" style={{ "--d": ".25s" } as CSSProperties} id="filter-bar">
      <StaticSelect
        dataKey="state"
        ariaLabel="issue / tmux 状態で絞り込み"
        placeholder="state"
        options={[
          ["open", "open"],
          ["closed", "closed"],
          ["live", "live"],
          ["stale", "stale"],
        ]}
        onPick={onPickToken}
      />
      <StaticSelect
        dataKey="run"
        ariaLabel="agent 実行状態で絞り込み"
        placeholder="run"
        options={[
          ["running", "running"],
          ["done", "done"],
        ]}
        onPick={onPickToken}
      />
      <StaticSelect
        dataKey="ci"
        ariaLabel="CI 結果で絞り込み"
        placeholder="ci"
        options={[
          ["pass", "pass"],
          ["fail", "fail"],
          ["pending", "pending"],
        ]}
        onPick={onPickToken}
      />
      <StaticSelect
        dataKey="dirty"
        ariaLabel="worktree の dirty 状態で絞り込み"
        placeholder="dirty"
        options={[
          ["yes", "yes"],
          ["no", "no"],
        ]}
        onPick={onPickToken}
      />
      <StaticSelect
        dataKey="live"
        ariaLabel="tmux ペイン生死で絞り込み"
        placeholder="live"
        options={[
          ["yes", "yes"],
          ["no", "no"],
        ]}
        onPick={onPickToken}
      />
      <StaticSelect
        dataKey="pr"
        ariaLabel="PR 状態で絞り込み"
        placeholder="pr"
        options={[
          ["merged", "merged"],
          ["open", "open"],
          ["closed", "closed"],
          ["none", "none"],
        ]}
        onPick={onPickToken}
      />
      <DynamicSelect
        id="f-agent"
        dataKey="agent"
        ariaLabel="agent で絞り込み"
        placeholder="agent"
        options={agents.map((a) => [a, a] as const)}
        onPick={onPickToken}
      />
      <DynamicSelect
        id="f-wave"
        dataKey="wave"
        ariaLabel="wave で絞り込み"
        placeholder="wave"
        options={waves.map((w) => [String(w), `w${w}`] as const)}
        onPick={onPickToken}
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
