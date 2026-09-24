import type { PaneView, PRRef, PRStack, Snapshot } from "../../transport/types";

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

/* snapshot 全体(フィルタ前)から引く索引。フィルタ後の行から組むと、隠れた行が
 * 持つ層の行名が消える。この repository の PR だけを番号で持つ — native stack も
 * 推定連鎖も repository をまたがない。 */
export interface StackIndex {
  repo: string;
  prs: Map<number, { pr: PRRef; owners: PaneView[] }>;
  /* native stack の番号ごとの、snapshot にある所属 PR。 */
  members: Map<number, PRRef[]>;
  /* 推定連鎖の候補。head branch ごとに 1 本(先に見つかった open の PR)。 */
  byHead: Map<string, PRRef>;
  byBase: Map<string, PRRef[]>;
}

export function sameRepo(a: string | undefined, repo: string): boolean {
  return !!repo && a?.toLowerCase() === repo.toLowerCase();
}

export function buildStackIndex(snap: Snapshot | null): StackIndex {
  const repo = snap?.repo ?? "";
  const prs = indexPrs(snap, repo);
  const byHead = chainHeads(prs, repo);
  return { repo, prs, members: stackMembers(prs), byHead, byBase: groupByBase(byHead) };
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

function stackMembers(prs: StackIndex["prs"]): Map<number, PRRef[]> {
  const out = new Map<number, PRRef[]>();
  for (const { pr } of prs.values()) {
    if (!pr.stack) continue;
    out.set(pr.stack.number, [...(out.get(pr.stack.number) ?? []), pr]);
  }
  return out;
}

/* 推定連鎖に入れてよい PR。open の PR だけ — マージ済みの PR を下の層に数えると、
 * develop → main のような長寿命 branch の PR が、そこを base にする PR すべての
 * 下に付いてしまう。native stack の PR は GitHub の答えがあるので混ぜない。fork の
 * head は同名 branch と取り違える。 */
function chainable(pr: PRRef, repo: string): boolean {
  if (pr.stack || !pr.headRef || !pr.baseRef) return false;
  return (pr.state ?? "").toUpperCase() === "OPEN" && sameRepo(pr.headRepo, repo);
}

function chainHeads(prs: StackIndex["prs"], repo: string): Map<string, PRRef> {
  const out = new Map<string, PRRef>();
  for (const { pr } of prs.values()) {
    const head = pr.headRef ?? "";
    if (chainable(pr, repo) && !out.has(head)) out.set(head, pr);
  }
  return out;
}

function groupByBase(byHead: Map<string, PRRef>): Map<string, PRRef[]> {
  const out = new Map<string, PRRef[]>();
  for (const pr of byHead.values()) {
    const base = pr.baseRef ?? "";
    out.set(base, [...(out.get(base) ?? []), pr]);
  }
  return out;
}

function layer(position: number, pr: PRRef, index: StackIndex): StackLayer {
  const hit = index.prs.get(pr.number);
  return { position, pr: hit?.pr ?? pr, owners: hit?.owners ?? [] };
}

/* entries は取得前・失敗時には無く、20 層で打ち切られもする。欠けた層は snapshot
 * にある同じ stack の PR(自分を含む)で埋める — さもないと行の PR がドロワーから
 * 消える。総数は stack.size が持っている。 */
function nativeStack(stack: PRStack, pr: PRRef, index: StackIndex): StackView {
  const entries = [...(stack.entries ?? [])];
  for (const m of [pr, ...(index.members.get(stack.number) ?? [])]) {
    if (entries.some((e) => e.pr.number === m.number)) continue;
    entries.push({ position: m.stack?.position ?? 0, pr: m });
  }
  const layers = entries
    .map((e) => layer(e.position, e.pr, index))
    .sort((a, b) => a.position - b.position);
  return {
    kind: "native",
    number: stack.number,
    baseRef: stack.baseRef,
    size: stack.size,
    position: stack.position,
    layers,
  };
}

/* 自分の head を base に持つ PR が、ちょうど 1 本ならそれ。2 本以上は木になり、
 * 1 本の柱には描けない。上下どちらへたどるときもこの 1 本だけでつなぐので、PR は
 * 高々 1 つの連鎖にしか入らない — 同じ PR がドロワーに 2 度出ることも、連鎖の
 * 選び方で行の PR が消えることもない。 */
function soleChild(pr: PRRef, index: StackIndex): PRRef | undefined {
  const ups = index.byBase.get(pr.headRef ?? "") ?? [];
  return ups.length === 1 ? ups[0] : undefined;
}

/* base を head に持つ PR を下へたどる。戻り値は下の層から順。 */
function walkDown(pr: PRRef, index: StackIndex, seen: Set<number>): PRRef[] {
  const out: PRRef[] = [];
  let cur = pr;
  let next = index.byHead.get(pr.baseRef ?? "");
  while (next && !seen.has(next.number) && soleChild(next, index)?.number === cur.number) {
    seen.add(next.number);
    out.unshift(next);
    cur = next;
    next = index.byHead.get(next.baseRef ?? "");
  }
  return out;
}

function walkUp(pr: PRRef, index: StackIndex, seen: Set<number>): PRRef[] {
  const out: PRRef[] = [];
  let next = soleChild(pr, index);
  while (next && !seen.has(next.number)) {
    seen.add(next.number);
    out.push(next);
    next = soleChild(next, index);
  }
  return out;
}

/* 連鎖を持てる PR か。同じ head に PR が複数あるときは代表(byHead)だけ — そう
 * しないと 1 行に同じ連鎖が 2 つ並ぶ。候補かどうかは行が持つコピーで判定する。
 * issue 側と branch 側の取得は別の時刻に着地するので、索引のコピーと状態が違う
 * ことがある。 */
function chainRep(pr: PRRef, index: StackIndex): boolean {
  return chainable(pr, index.repo) && index.byHead.get(pr.headRef ?? "")?.number === pr.number;
}

/* 循環(a → b → a)には base branch が無く、どちらの端から見ても別の柱になる。 */
function isCycle(chain: PRRef[]): boolean {
  const base = chain[0]?.baseRef;
  return chain.some((p) => p.headRef === base);
}

function inferredStack(pr: PRRef, index: StackIndex): StackView | null {
  if (!chainRep(pr, index)) return null;
  const seen = new Set([pr.number]);
  const below = walkDown(pr, index, seen);
  const chain = [...below, pr, ...walkUp(pr, index, seen)];
  if (chain.length < 2 || isCycle(chain)) return null;
  return {
    kind: "inferred",
    baseRef: chain[0]?.baseRef ?? "",
    size: chain.length,
    position: below.length + 1,
    layers: chain.map((p, i) => layer(i + 1, p, index)),
  };
}

/* PR が属する stack。native を推定より優先する。 */
export function stackOf(pr: PRRef, index: StackIndex): StackView | null {
  if (!sameRepo(pr.baseRepo, index.repo)) return null;
  return pr.stack ? nativeStack(pr.stack, pr, index) : inferredStack(pr, index);
}

export type PrGroup = { key: string; stack: StackView } | { key: string; pr: PRRef };

/* ドロワーの並び。stack に属する PR は stack ごとに 1 ブロックへまとめ、残りは
 * 今までどおり 1 行ずつ。順序は wire 順での初出順。 */
export function groupPrs(prs: PRRef[], index: StackIndex): PrGroup[] {
  const groups = new Map<string, PrGroup>();
  for (const pr of prs) {
    const stack = stackOf(pr, index);
    const group: PrGroup = stack
      ? { key: stackKey(stack), stack }
      : { key: `pr:${pr.baseRepo ?? ""}#${pr.number}`, pr };
    if (!groups.has(group.key)) groups.set(group.key, group);
  }
  return [...groups.values()];
}

function stackKey(stack: StackView): string {
  if (stack.kind === "native") return `native:${stack.number}`;
  return `inferred:${stack.layers[0]?.pr.number}`;
}
