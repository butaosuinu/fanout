import type { PaneView, PRRef, PRStack, PRStackEntry, Snapshot } from "../../transport/types";

/* stacked PR の 1 層。pr は行のコピーがあればそれ(CI などの信号を全部持つ)、
 * 無ければ stack 取得の軽量コピー。owners はその PR を prs に持つ行。 */
export interface StackLayer {
  position: number;
  pr: PRRef;
  owners: PaneView[];
}

/* native は GitHub の stack、inferred は base / head のつながりから推定した連鎖。
 * layers は position 昇順(1 = base に最も近い)。 */
export interface StackView {
  kind: "native" | "inferred";
  number?: number;
  baseRef: string;
  size: number;
  position: number;
  layers: StackLayer[];
}

/* PR が載る柱と、柱の中での位置。 */
interface Placement {
  view: StackView;
  position: number;
}

/* snapshot 全体(フィルタ前)から引く索引。フィルタ後の行から組むと、隠れた行が
 * 持つ層の行名が消える。この repository の PR だけを番号で持つ — native stack も
 * 推定連鎖も repository をまたがない。
 *
 * 柱はここで一度だけ組み、PR 番号ごとに引く。どの PR も高々 1 本の柱に載り、柱の
 * 層はどれも同じ柱を指す。行ごとに柱を組むと、取得時刻の違うコピー(古い
 * entries、retarget 前後の base)から別々の柱ができ、同じ PR が 2 度出たり、行の
 * PR が消えたりする。 */
export interface StackIndex {
  repo: string;
  prs: Map<number, { pr: PRRef; owners: PaneView[] }>;
  /* native stack に載っていると分かっている PR の番号。推定には混ぜない。 */
  native: Set<number>;
  placed: Map<number, Placement>;
}

export function sameRepo(a: string | undefined, repo: string): boolean {
  return !!repo && a?.toLowerCase() === repo.toLowerCase();
}

export function buildStackIndex(snap: Snapshot | null): StackIndex {
  const repo = snap?.repo ?? "";
  const prs = indexPrs(snap, repo);
  const placed = new Map<number, Placement>();
  placeNative({ prs, placed, views: new Map() });
  const native = new Set(placed.keys());
  placeInferred(prs, { repo, native, placed });
  return { repo, prs, native, placed };
}

export const EMPTY_STACK_INDEX = buildStackIndex(null);

function indexPrs(snap: Snapshot | null, repo: string): StackIndex["prs"] {
  const out: StackIndex["prs"] = new Map();
  const rows = (snap?.sessions ?? [])
    .flatMap((s) => s.panes ?? [])
    .flatMap((pane) => (pane.prs ?? []).map((pr) => ({ pane, pr })));
  for (const { pane, pr } of rows) {
    if (!sameRepo(pr.baseRepo, repo)) continue;
    const hit = out.get(pr.number) ?? { pr, owners: [] };
    if (!hit.owners.includes(pane)) hit.owners.push(pane);
    out.set(pr.number, hit);
  }
  return out;
}

/* 層の PR は行のコピーがあればそれ(CI などの信号を全部持つ)、無ければ stack 取得の
 * 軽量コピー。 */
function layer(position: number, pr: PRRef, prs: StackIndex["prs"]): StackLayer {
  const hit = prs.get(pr.number);
  return { position, pr: hit?.pr ?? pr, owners: hit?.owners ?? [] };
}

interface NativePlacing {
  prs: StackIndex["prs"];
  placed: Map<number, Placement>;
  views: Map<number, StackView>;
}

/* native stack を番号ごとに 1 本の柱にする。先に各 PR が自分で読んだ所属を置き、
 * entries はその後で空きを埋めるだけ — 読み取りに失敗した PR は最後に分かった
 * stack を保つので、古い entries と新しい所属が食い違いうる。entries は取得前・
 * 失敗時には無く、20 層で打ち切られもするので、snapshot にある所属 PR が欠けた層を
 * 埋める。 */
function placeNative(ctx: NativePlacing): void {
  const members = [...ctx.prs.values()].flatMap(({ pr }) =>
    pr.stack ? [{ pr, stack: pr.stack }] : [],
  );
  for (const { pr, stack } of members) place(ctx, stack, { position: stack.position, pr });
  for (const { stack } of members) {
    for (const e of stack.entries ?? []) place(ctx, stack, e);
  }
  for (const view of ctx.views.values()) view.layers.sort((a, b) => a.position - b.position);
}

function place(ctx: NativePlacing, stack: PRStack, e: PRStackEntry): void {
  if (ctx.placed.has(e.pr.number)) return;
  const view = ctx.views.get(stack.number) ?? {
    kind: "native",
    number: stack.number,
    baseRef: stack.baseRef,
    size: 0,
    position: 0,
    layers: [],
  };
  view.size = Math.max(view.size, stack.size);
  view.layers.push(layer(e.position, e.pr, ctx.prs));
  ctx.views.set(stack.number, view);
  ctx.placed.set(e.pr.number, { view, position: e.position });
}

/* 推定連鎖に入れてよい PR。open の PR だけ — マージ済みの PR を下の層に数えると、
 * develop → main のような長寿命 branch の PR が、そこを base にする PR すべての
 * 下に付いてしまう。native stack に載る PR は GitHub の答えがあるので混ぜない —
 * 混ぜると同じ PR が native と推定の 2 つの stack map に出る。fork の head は
 * 同名 branch と取り違える。 */
function chainable(pr: PRRef, repo: string, native: Set<number>): boolean {
  if (native.has(pr.number) || !pr.headRef || !pr.baseRef) return false;
  return (pr.state ?? "").toUpperCase() === "OPEN" && sameRepo(pr.headRepo, repo);
}

interface InferredPlacing {
  repo: string;
  native: Set<number>;
  placed: Map<number, Placement>;
}

/* 推定連鎖も索引のコピーだけで組む。行のコピーを混ぜると、retarget の前後で base が
 * 違うコピーから別々の連鎖ができる。 */
function placeInferred(prs: StackIndex["prs"], ctx: InferredPlacing): void {
  const byHead = chainHeads(prs, ctx.repo, ctx.native);
  const links = { byHead, byBase: groupByBase(byHead) };
  for (const pr of byHead.values()) {
    if (ctx.placed.has(pr.number)) continue;
    const chain = chainOf(pr, links);
    if (chain) placeChain(chain, prs, ctx.placed);
  }
}

/* 推定連鎖の候補。head branch ごとに 1 本(先に見つかった open の PR)。 */
function chainHeads(prs: StackIndex["prs"], repo: string, native: Set<number>): Map<string, PRRef> {
  const out = new Map<string, PRRef>();
  for (const { pr } of prs.values()) {
    const head = pr.headRef ?? "";
    if (chainable(pr, repo, native) && !out.has(head)) out.set(head, pr);
  }
  return out;
}

function groupByBase(byHead: Map<string, PRRef>): Map<string, PRRef[]> {
  const out = new Map<string, PRRef[]>();
  for (const pr of byHead.values()) {
    const base = pr.baseRef ?? "";
    const list = out.get(base);
    if (list) list.push(pr);
    else out.set(base, [pr]);
  }
  return out;
}

interface Links {
  byHead: Map<string, PRRef>;
  byBase: Map<string, PRRef[]>;
}

/* 自分の head を base に持つ PR が、ちょうど 1 本ならそれ。2 本以上は木になり、
 * 1 本の柱には描けない。上下どちらへたどるときもこの 1 本だけでつなぐので、連鎖は
 * どの層から組んでも同じになる。 */
function soleChild(pr: PRRef, links: Links): PRRef | undefined {
  const ups = links.byBase.get(pr.headRef ?? "") ?? [];
  return ups.length === 1 ? ups[0] : undefined;
}

/* base を head に持つ PR を下へたどる。戻り値は下の層から順。 */
function walkDown(pr: PRRef, links: Links, seen: Set<number>): PRRef[] {
  const out: PRRef[] = [];
  let cur = pr;
  let next = links.byHead.get(pr.baseRef ?? "");
  while (next && !seen.has(next.number) && soleChild(next, links)?.number === cur.number) {
    seen.add(next.number);
    out.unshift(next);
    cur = next;
    next = links.byHead.get(next.baseRef ?? "");
  }
  return out;
}

function walkUp(pr: PRRef, links: Links, seen: Set<number>): PRRef[] {
  const out: PRRef[] = [];
  let next = soleChild(pr, links);
  while (next && !seen.has(next.number)) {
    seen.add(next.number);
    out.push(next);
    next = soleChild(next, links);
  }
  return out;
}

/* 循環(a → b → a)には base branch が無い。 */
function isCycle(chain: PRRef[]): boolean {
  const base = chain[0]?.baseRef;
  return chain.some((p) => p.headRef === base);
}

function chainOf(pr: PRRef, links: Links): PRRef[] | null {
  const seen = new Set([pr.number]);
  const chain = [...walkDown(pr, links, seen), pr, ...walkUp(pr, links, seen)];
  return chain.length < 2 || isCycle(chain) ? null : chain;
}

function placeChain(chain: PRRef[], prs: StackIndex["prs"], placed: Map<number, Placement>): void {
  const view: StackView = {
    kind: "inferred",
    baseRef: chain[0]?.baseRef ?? "",
    size: chain.length,
    position: 0,
    layers: chain.map((p, i) => layer(i + 1, p, prs)),
  };
  chain.forEach((p, i) => placed.set(p.number, { view, position: i + 1 }));
}

/* PR が載る柱。推定の柱は索引のコピーで組むので、行のコピーが候補でない(取得時刻の
 * 違いでマージ済みに見えるなど)行では出さない。 */
export function stackOf(pr: PRRef, index: StackIndex): StackView | null {
  if (!sameRepo(pr.baseRepo, index.repo)) return null;
  const hit = index.placed.get(pr.number);
  if (!hit) return null;
  if (hit.view.kind === "inferred" && !chainable(pr, index.repo, index.native)) return null;
  return { ...hit.view, position: hit.position };
}

export type PrGroup = { key: string; stack: StackView } | { key: string; pr: PRRef };

/* ドロワーの並び。stack に属する PR は stack ごとに 1 ブロックへまとめ、残りは
 * 今までどおり 1 行ずつ。順序は wire 順での初出順。同じ柱の PR は同じ層を持つので、
 * 先に見つかった 1 つを描けば全員が載る。
 *
 * 柱に層として載る PR は、平坦な行に重ねない。推定の柱は行のコピーでは出さないこと
 * があり、そのとき同じ PR が平坦な行と層の両方に出て、Delete branch も 2 つ並ぶ。 */
export function groupPrs(prs: PRRef[], index: StackIndex): PrGroup[] {
  const views = prs.map((pr) => ({ pr, stack: stackOf(pr, index) }));
  const layered = new Set(views.flatMap((v) => v.stack?.layers.map((l) => l.pr.number) ?? []));
  const groups = new Map<string, PrGroup>();
  for (const { pr, stack } of views) {
    if (!stack && layered.has(pr.number) && sameRepo(pr.baseRepo, index.repo)) continue;
    const group = groupOf(pr, stack);
    if (!groups.has(group.key)) groups.set(group.key, group);
  }
  return [...groups.values()];
}

function groupOf(pr: PRRef, stack: StackView | null): PrGroup {
  if (stack) return { key: stackKey(stack), stack };
  return { key: `pr:${pr.baseRepo ?? ""}#${pr.number}`, pr };
}

function stackKey(stack: StackView): string {
  if (stack.kind === "native") return `native:${stack.number}`;
  return `inferred:${stack.layers[0]?.pr.number}`;
}
