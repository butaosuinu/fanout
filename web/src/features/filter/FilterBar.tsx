import type { MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import { useMemo, type CSSProperties } from "react";
import { filterTokens, tokenForKey } from "./filter";
import { FilterDropdown, type Option } from "./FilterDropdown";
import { AGENT_STATES } from "../sessions/badges";

/* 静的キーのドロップダウン定義。options をモジュール定数にすることで
 * memo(FilterDropdown) が snapshot tick の再レンダーをスキップできる。
 *
 * options の value / label は英語のまま据え置く — これはフィルタ構文そのもので
 * (state:open, ci:fail)、表示だけ訳すと入力欄に打つ語と一致しなくなる。
 * 翻訳するのは説明文である ariaLabel だけ。モジュール定数は import 時に一度しか
 * 評価されないので、翻訳済み文字列ではなく descriptor を置く。 */
const STATIC_DROPDOWNS: readonly {
  key: string;
  ariaLabel: MessageDescriptor;
  options: readonly Option[];
}[] = [
  {
    key: "state",
    ariaLabel: msg`issue / runtime 状態で絞り込み`,
    options: [
      ["open", "open"],
      ["closed", "closed"],
      ["live", "live"],
      ["stale", "stale"],
      ["queued", "queued"], // 未開始(synthetic)行の runtime 状態
      ["deferred", "deferred"], // blocker 待ちの未開始行
    ],
  },
  {
    key: "backend",
    ariaLabel: msg`runtime backend で絞り込み`,
    options: [
      ["tmux", "tmux"],
      ["herdr", "herdr"],
    ],
  },
  {
    key: "run",
    ariaLabel: msg`agent 実行状態で絞り込み`,
    // 6 値契約の単一情報源(features/sessions/badges.tsx AGENT_STATE_CLASSES)から
    // 順序ごと導出。
    options: AGENT_STATES.map((s) => [s, s] as const),
  },
  {
    key: "ci",
    ariaLabel: msg`CI 結果で絞り込み`,
    options: [
      ["pass", "pass"],
      ["fail", "fail"],
      ["pending", "pending"],
    ],
  },
  {
    key: "dirty",
    ariaLabel: msg`worktree の dirty 状態で絞り込み`,
    options: [
      ["yes", "yes"],
      ["no", "no"],
    ],
  },
  {
    key: "live",
    ariaLabel: msg`runtime ペイン生死で絞り込み`,
    options: [
      ["yes", "yes"],
      ["no", "no"],
    ],
  },
  {
    key: "pr",
    ariaLabel: msg`PR 状態で絞り込み`,
    options: [
      ["merged", "merged"],
      ["open", "open"],
      ["closed", "closed"],
      ["none", "none"],
    ],
  },
  {
    key: "review",
    ariaLabel: msg`PR のレビュー状態で絞り込み`,
    options: [
      ["approved", "approved"],
      ["changes-requested", "changes-requested"],
      ["review-required", "review-required"],
      ["none", "none"],
    ],
  },
];

/* チップの除去ラベル。t マクロを使う — <Trans> は {変数} 前後の空白を落とすため。 */
function removeTokenLabel(token: string): MessageDescriptor {
  return msg`フィルタ ${{ token }} を外す`;
}

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
  const { i18n, t } = useLingui();
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
        <FilterDropdown
          key={d.key}
          {...dd(d.key)}
          ariaLabel={i18n._(d.ariaLabel)}
          options={d.options}
        />
      ))}
      <FilterDropdown
        {...dd("agent")}
        ariaLabel={t`agent で絞り込み`}
        options={agentOptions}
        searchable
      />
      <FilterDropdown
        {...dd("wave")}
        ariaLabel={t`wave で絞り込み`}
        options={waveOptions}
        searchable
      />
      <span id="chips" role="list" aria-label={t`適用中のフィルタ`}>
        {tokens.map((tok) => (
          <button
            key={tok}
            type="button"
            className="chip"
            role="listitem"
            aria-label={i18n._(removeTokenLabel(tok))}
            onClick={() => onRemoveToken(tok)}
          >
            {tok}
            <span className="x" aria-hidden="true">
              ×
            </span>
          </button>
        ))}
      </span>
    </div>
  );
}
