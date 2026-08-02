import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type RefObject,
} from "react";
import { useSystemThemeSync } from "../hooks/useSettings";
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
import { diffQuery, findPaneEntry, paneLabel, rowKey } from "../lib/pane";
import { sortPanes, type SortDir } from "../lib/sort";
import type { PaneView, Snapshot } from "../lib/types";
import { Drawer } from "./Drawer";
import { FilterBar } from "./FilterBar";
import { Hud } from "./Hud";
import { Nav } from "./Nav";
import { SessionSection, type SessionItem } from "./SessionSection";
import { SettingsModal } from "./SettingsModal";

/* @pierre/diffs(Shiki 込み)は重いので遅延 chunk に隔離し、diff を開くまで
 * 初回ロードのパスに乗せない。 */
const DiffOverlay = lazy(() => import("./DiffOverlay").then((m) => ({ default: m.DiffOverlay })));

interface DiffTarget {
  key: string; // rowKey — snapshot から消えたら閉じるための識別子
  title: string;
  query: Record<string, string>;
}

/* 背面で popup が開いているか。
 *
 * capture 段は React の handler より先に走るので、開いている popup の Escape を
 * 横取りすると 1 回のキーで popup と diff 起動の両方が消える。overlay が出た後は
 * 「フォーカスが自分の中にあるか」で判定できるが、待機中は自分の DOM が無い。
 *
 * フォーカス先から `closest()` で辿るだけでは足りない — FilterDropdown は
 * `aria-expanded` を trigger に付け、フォーカスは兄弟の popover(検索 input や
 * option)へ移すので、祖先を辿っても trigger に当たらない。開いている trigger が
 * 文書内にあるかで見る(popover は blur で閉じるので、開いている = そこに
 * フォーカスがある)。 */
function backgroundPopupOpen(): boolean {
  if (document.querySelector('[aria-expanded="true"]')) return true;
  const active = document.activeElement;
  return active instanceof Element && active.closest('[role="dialog"],[role="listbox"]') !== null;
}

/* lazy chunk の解決待ち。Escape で起動を取り消せるようにするためだけに存在する
 * — fallback が空だと、この間の Escape は(Drawer 起点なら)Drawer だけを閉じて
 * diffTarget が残り、chunk が解決した瞬間に「閉じたはず」の diff が出てくる。
 * 表セル起点では Escape 自体が効かない。見た目は出さない(短い窓なので、空の
 * パネルが一瞬光るほうが煩わしい)。 */
export function DiffPending({ enabled, onCancel }: { enabled: boolean; onCancel: () => void }) {
  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (backgroundPopupOpen()) return;
      e.preventDefault();
      onCancel();
    };
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [enabled, onCancel]);
  return null;
}

/* 生きている(DOM に繋がっている)最初の起点へフォーカスを戻す。detached な
 * 要素への focus() は無言で何も起きないので、候補を順に試す。
 *
 * 最後は必ず Nav の歯車へ落とす。起点はいくらでも消える(diff を開いた Drawer を
 * 先に閉じた、フィルタ変更で起点行が消えた、対象 pane が snapshot から消えて
 * diff ごと unmount された)ので、存続する要素を末尾に置かないとフォーカスが
 * どこにも戻らず、キーボードの現在位置が失われる。 */
function focusFirstConnected(refs: RefObject<HTMLElement | null>[]) {
  const candidates = [...refs.map((r) => r.current), document.getElementById("settings-open")];
  for (const el of candidates) {
    if (el?.isConnected) {
      el.focus();
      return;
    }
  }
}

function sameQuery(a: Record<string, string>, b: Record<string, string>): boolean {
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every((k) => a[k] === b[k]);
}

function parentsOf(snap: Snapshot | null): Set<string> {
  return new Set((snap?.sessions ?? []).map((s) => String(s.parent ?? "")));
}

export function App() {
  /* 設定モーダルと diff オーバーレイは開いている間しか外観 store を購読しない。
   * ここで常駐購読を張り、システム追従中の OS 配色変更を取りこぼさない。 */
  useSystemThemeSync();
  const token = useMemo(() => readToken(), []);
  const { snap, conn } = useSnapshot(token);
  const [filter, setFilter] = useState("");
  const [sortKey, setSortKey] = useState("issueNum");
  const [sortDir, setSortDir] = useState<SortDir>(1);
  const [selected, setSelected] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [diffTarget, setDiffTarget] = useState<DiffTarget | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  /* diff が背面を覆っているか。実寸に依存するのでオーバーレイから受け取る */
  const [diffCovering, setDiffCovering] = useState(false);
  const rowRefs = useRef(new Map<string, HTMLTableRowElement>());
  /* モーダルを開いた起点要素。閉じたときにフォーカスを戻す(diff は表のセル
   * からも Drawer のボタンからも開くので、ref 固定ではなく起点を控える)。
   * 設定は diff オーバーレイの上にも開けるので、起点は別々に持つ。 */
  const diffOriginRef = useRef<HTMLElement | null>(null);
  const settingsOriginRef = useRef<HTMLElement | null>(null);

  /* 選択中ペインが snapshot から消えたら通知して閉じる。意図的に deps は snap
   * のみ — 旧実装の「draw() ごとの syncDrawer」と同じく snapshot 到着時に
   * 評価する(選択変更だけでは notice を消さない)。 */
  useEffect(() => {
    if (!snap) return;
    if (selected && !findPaneEntry(snap, selected)) {
      setNotice(`${selected} は snapshot から消えたため詳細を閉じました`);
      setSelected(null);
    } else {
      setNotice(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [snap]);

  /* diff オーバーレイの対象行が消えた場合も同じく閉じる(表から直接開いた
   * ときは Drawer が開いていないので、選択の後始末とは別に要る)。
   *
   * 行の存在だけでは足りない。記録済み pane を cleanup すると sessionview は
   * 同じ rowKey のまま notStarted の synthetic 行を作り直すので、findPaneEntry は
   * 成功し続ける。その行には worktree も diff 導線も無いのに、cleanup 前に取った
   * patch を現在の差分として出したままになる。同じ query をまだ組めるかまで見る。 */
  useEffect(() => {
    if (!snap || !diffTarget) return;
    const entry = findPaneEntry(snap, diffTarget.key);
    const query = entry ? diffQuery(entry.parent, entry.pane) : null;
    if (!query || !sameQuery(query, diffTarget.query)) setDiffTarget(null);
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
  const selectedEntry = findPaneEntry(snap, selected);
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
  const openDiff = useCallback((parent: string, pane: PaneView) => {
    const query = diffQuery(parent, pane);
    if (!query) return;
    diffOriginRef.current = document.activeElement as HTMLElement | null;
    setDiffTarget({ key: rowKey(parent, pane), title: paneLabel(pane), query });
  }, []);
  const closeDiff = useCallback(() => setDiffTarget(null), []);
  const openSettings = useCallback(() => {
    settingsOriginRef.current = document.activeElement as HTMLElement | null;
    setSettingsOpen(true);
  }, []);
  const closeSettings = useCallback(() => setSettingsOpen(false), []);
  /* モーダルを閉じたらフォーカスを起点へ戻す。モーダル自身の cleanup ではやらない
   * — 起点が diff の中のボタンだと、その時点では diff がまだ inert で
   * (sibling の effect cleanup は後)実ブラウザが focus() を拒否する。親の
   * effect は子より後に走るので、ここなら inert 解除の後になる。
   *
   * diff 側をここに置くのは、lazy chunk の解決待ちに対象 pane が snapshot から
   * 消えると DiffOverlay が一度も mount されず、その unmount 経路では復帰処理が
   * 走らないため(fallback の DiffPending が消えるだけ)。 */
  const settingsWasOpen = useRef(settingsOpen);
  useEffect(() => {
    if (settingsWasOpen.current && !settingsOpen) {
      focusFirstConnected([settingsOriginRef, diffOriginRef]);
    }
    settingsWasOpen.current = settingsOpen;
  }, [settingsOpen]);

  const diffWasOpen = useRef(diffTarget !== null);
  useEffect(() => {
    const open = diffTarget !== null;
    if (diffWasOpen.current && !open) focusFirstConnected([diffOriginRef]);
    diffWasOpen.current = open;
  }, [diffTarget]);

  const registerRow = (key: string, el: HTMLTableRowElement | null) => {
    if (el) rowRefs.current.set(key, el);
    else rowRefs.current.delete(key);
  };

  return (
    <>
      <Nav
        repo={repo}
        projectRoot={snap?.projectRoot ?? ""}
        conn={conn}
        onOpenSettings={openSettings}
      />
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
                placeholder="filter — 自由語 or backend:herdr state:open run:plan agent:claude wave:1 ci:fail"
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
                  onOpenDiff={openDiff}
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
        {selectedEntry && selected && (
          <Drawer
            key={selected}
            pane={selectedEntry.pane}
            parent={selectedEntry.parent}
            repo={repo}
            token={token}
            diffCovering={diffCovering}
            onOpenDiff={openDiff}
            onClose={closeDrawer}
          />
        )}
      </div>
      {diffTarget && (
        <Suspense fallback={<DiffPending enabled={!settingsOpen} onCancel={closeDiff} />}>
          <DiffOverlay
            title={diffTarget.title}
            query={diffTarget.query}
            token={token}
            anchorKey={selected}
            suppressed={settingsOpen}
            onCoveringChange={setDiffCovering}
            escapeEnabled={!settingsOpen}
            onOpenSettings={openSettings}
            onClose={closeDiff}
          />
        </Suspense>
      )}
      {settingsOpen && <SettingsModal onClose={closeSettings} />}
    </>
  );
}
