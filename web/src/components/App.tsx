import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useSnapshot } from "../hooks/useSnapshot";
import { readToken } from "../lib/api";
import {
  filterTokens,
  matches,
  parseQuery,
  removeToken,
  replaceToken,
  stripKey,
} from "../lib/filter";
import { clock, degradedMessages } from "../lib/format";
import { deriveAgents, deriveWaves } from "../lib/options";
import { findPane } from "../lib/pane";
import { sortPanes, type SortDir } from "../lib/sort";
import type { Snapshot } from "../lib/types";
import { Drawer } from "./Drawer";
import { FilterBar } from "./FilterBar";
import { Hud } from "./Hud";
import { Nav } from "./Nav";
import { SessionSection, type SessionItem } from "./SessionSection";

function parentsOf(snap: Snapshot | null): Set<string> {
  return new Set((snap?.sessions ?? []).map((s) => String(s.parent ?? "")));
}

export function App() {
  const token = useMemo(() => readToken(), []);
  const { snap, conn } = useSnapshot(token);
  const [filter, setFilter] = useState("");
  const [sortKey, setSortKey] = useState("issueNum");
  const [sortDir, setSortDir] = useState<SortDir>(1);
  const [selected, setSelected] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const rowRefs = useRef(new Map<string, HTMLTableRowElement>());

  /* 選択中ペインが snapshot から消えたら通知して閉じる。意図的に deps は snap
   * のみ — 旧実装の「draw() ごとの syncDrawer」と同じく snapshot 到着時に
   * 評価する(選択変更だけでは notice を消さない)。 */
  useEffect(() => {
    if (!snap) return;
    if (selected && !findPane(snap, selected)) {
      setNotice(`${selected} は snapshot から消えたため詳細を閉じました`);
      setSelected(null);
    } else {
      setNotice(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [snap]);

  /* rise 入場アニメは「前 snapshot に存在しなかった parent」のみ。フィルタ操作
   * による再 mount はアニメしない(旧実装の animate=true/false の意味論)。
   * 初回 snapshot は prev が null なので全セクションが rise する。 */
  const prevParentsRef = useRef<Set<string> | null>(null);
  const riseParents = useMemo(() => {
    const prev = prevParentsRef.current;
    const cur = parentsOf(snap);
    if (!prev) return cur;
    return new Set([...cur].filter((p) => !prev.has(p)));
  }, [snap]);
  useEffect(() => {
    prevParentsRef.current = parentsOf(snap);
  }, [snap]);

  const terms = parseQuery(filter);
  let shown = 0;
  let total = 0;
  const items: SessionItem[] = [];
  for (const s of snap?.sessions ?? []) {
    const all = s.panes ?? [];
    total += all.length;
    const panes = all.filter((p) => matches(p, terms));
    shown += panes.length;
    if (!panes.length) continue;
    const parent = String(s.parent ?? "");
    items.push({
      parent,
      panes: sortPanes(panes, sortKey, sortDir),
      rollup: s.rollup,
      rise: riseParents.has(parent),
    });
  }

  const repo = snap?.repo ?? "";
  const selectedPane = findPane(snap, selected);
  const msgs = degradedMessages(snap?.degraded);
  const status = snap
    ? `${notice ? `${notice} · ` : ""}telemetry @ ${clock(snap.generatedAt)}`
    : "awaiting telemetry";

  const onSort = (key: string) => {
    if (sortKey === key) setSortDir((d) => (d === 1 ? -1 : 1));
    else {
      setSortKey(key);
      setSortDir(1);
    }
  };
  /* 関数 setState + useCallback で identity を安定させ、memo(FilterDropdown)
   * が snapshot tick の再レンダーをスキップできるようにする */
  const onPickToken = useCallback((key: string, value: string) => {
    setFilter((f) => replaceToken(filterTokens(f), key, value).join(" "));
  }, []);
  const onClearKey = useCallback((key: string) => {
    setFilter((f) => stripKey(filterTokens(f), key).join(" "));
  }, []);
  const onRemoveToken = useCallback((tok: string) => {
    setFilter((f) => removeToken(filterTokens(f), tok).join(" "));
  }, []);
  const closeDrawer = () => {
    const key = selected;
    setSelected(null);
    if (key) rowRefs.current.get(key)?.focus(); // 起点の行へフォーカスを戻す
  };
  const registerRow = (key: string, el: HTMLTableRowElement | null) => {
    if (el) rowRefs.current.set(key, el);
    else rowRefs.current.delete(key);
  };

  return (
    <>
      <Nav repo={repo} projectRoot={snap?.projectRoot ?? ""} conn={conn} />
      <div className="shell">
        <div className="main-col">
          <div className="wrap">
            <Hud rollup={snap?.rollup} />
            <div className="banner" id="banner" role="status" hidden={!msgs.length}>
              {msgs.join(" · ")}
            </div>
            <div className="toolbar rise" style={{ "--d": ".2s" } as CSSProperties}>
              <input
                id="filter"
                type="search"
                autoComplete="off"
                spellCheck={false}
                placeholder="filter — 自由語 or state:open run:running agent:claude wave:1 ci:fail dirty:yes"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
              />
              <span id="filter-count">{snap ? `${shown} / ${total}` : ""}</span>
            </div>
            <FilterBar
              filter={filter}
              agents={deriveAgents(snap)}
              waves={deriveWaves(snap)}
              onPickToken={onPickToken}
              onClearKey={onClearKey}
              onRemoveToken={onRemoveToken}
            />
            <main id="sessions" aria-live="polite">
              {snap && items.length === 0 && (
                <section className="session">
                  <div className="empty">
                    <span className="ji">no panes in the breeze</span>
                    {total
                      ? "フィルタに一致するペインがありません"
                      : "アクティブなセッションがありません"}
                  </div>
                </section>
              )}
              {items.map((item) => (
                <SessionSection
                  key={item.parent}
                  item={item}
                  repo={repo}
                  sortKey={sortKey}
                  sortDir={sortDir}
                  selected={selected}
                  onSort={onSort}
                  onSelect={setSelected}
                  registerRow={registerRow}
                />
              ))}
            </main>
            <footer className="status-line">
              <span id="status">{status}</span>
              <span className="grow"></span>
              <span>read-only · 127.0.0.1</span>
            </footer>
          </div>
        </div>
        {selectedPane && selected && (
          <Drawer
            key={selected}
            pane={selectedPane}
            repo={repo}
            token={token}
            onClose={closeDrawer}
          />
        )}
      </div>
    </>
  );
}
