// この branch が新しく持ち込んだ複雑度だけを SARIF から取り出す。
//
// 変更行での絞り込みだけでは足りない。complexity 系の linter は違反位置を関数の
// **宣言行**として報告するため、既存関数の宣言行を触らずに本体へ if を足して
// しきい値を超えても、宣言行が変更行に入らず違反が消える。実測でも
// gocognit の finding が丸ごと落ちた。
//
// そこで merge-base 側の同じ解析結果をベースラインとして受け取り、指標が悪化した
// もの (または新しく現れたもの) だけを残す。同じ関数が同じ値のままなら、その行を
// 何行触っていても残さない — 450 行の既存関数を 1 行触っただけで PR が落ちるのを
// 避けるのが元々の要件で、ベースライン比較はそれも同時に満たす。
//
// 使い方:
//   node complexity-diff.mjs --current <sarif> [--base <sarif>] [--merge-base <sha>] [--root <dir>]
//
// --base があれば回帰比較、無ければ --merge-base からの変更行フィルタに退避する。
// 生き残った finding を stdout へ 1 行 1 件で出し、絞り込み後の SARIF を --current へ
// 書き戻す。呼び出し側は行数を数えて判定する。
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const args = process.argv.slice(2);
const opt = (name) => {
  const i = args.indexOf(name);
  return i >= 0 && i + 1 < args.length ? args[i + 1] : undefined;
};

const currentPath = opt("--current");
const basePath = opt("--base");
const mergeBase = opt("--merge-base");
const root = opt("--root") ?? process.cwd();

if (!currentPath) {
  console.error("usage: complexity-diff.mjs --current <sarif> [--base <sarif>] [--merge-base <sha>]");
  process.exit(2);
}

// stderr is swallowed: ls-files failing is how an untracked file is detected,
// and git's "did you forget to git add" noise would read as a CI error.
// core.quotePath は明示的に切る。既定の true では非 ASCII を含むパスが
// `"web/src/\347\224\273/Foo.ts"` の形で返り、パス突き合わせが全部外れる。
const git = (a) =>
  execFileSync("git", ["-C", root, "-c", "core.quotePath=false", ...a], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  });

let realRoot = root;
try {
  realRoot = fs.realpathSync(root);
} catch {
  /* keep the given root */
}

// Both sides must land on the same string or the baseline never matches. The
// linters emit absolute paths in some runs and repo-relative ones in others, and
// on macOS a repo under /tmp resolves to /private/tmp — realpath first, then
// relativize against the real root.
function relativeUri(uri) {
  // file:// URI は非 ASCII をパーセントエンコードするので必ず戻す。戻さないと
  // web/src/画面/Foo.ts が web/src/%E7%94%BB%E9%9D%A2/Foo.ts のまま git に渡り、
  // 追跡外と判定されてリネーム対応も変更行フィルタも外れる。
  let raw = uri.startsWith("file://") ? decodeURIComponent(new URL(uri).pathname) : uri;
  if (!path.isAbsolute(raw)) return raw;
  try {
    raw = fs.realpathSync(raw);
  } catch {
    /* the file may be gone; relativize what we have */
  }
  return path.relative(realRoot, raw);
}

// 未使用の抑制コメントは results ではなく invocations[].toolConfigurationNotifications
// に入る (実測)。results だけ読むと「未使用の抑制は ESLint が拾う」という契約が
// 成立しないので、こちらも finding として扱う。
const UNUSED_DISABLE_RULE = "eslint-unused-disable";

function readResults(file) {
  const sarif = JSON.parse(fs.readFileSync(file, "utf8"));
  const out = [];
  for (const run of sarif.runs ?? []) {
    const ruleIds = (run.tool?.driver?.rules ?? []).map((r) => r.id);
    for (const result of run.results ?? []) {
      const location = result.locations?.[0]?.physicalLocation;
      const text = result.message?.text ?? "";
      out.push({
        raw: result,
        run,
        file: location?.artifactLocation?.uri ? relativeUri(location.artifactLocation.uri) : "",
        line: location?.region?.startLine,
        rule: result.ruleId ?? ruleIds[result.ruleIndex] ?? "?",
        text,
      });
    }
    for (const invocation of run.invocations ?? []) {
      for (const note of invocation.toolConfigurationNotifications ?? []) {
        const text = note.message?.text ?? "";
        if (!/Unused eslint-disable directive/.test(text)) continue;
        const location = note.locations?.[0]?.physicalLocation;
        out.push({
          raw: note,
          run,
          notification: invocation,
          file: location?.artifactLocation?.uri ? relativeUri(location.artifactLocation.uri) : "",
          line: location?.region?.startLine,
          rule: UNUSED_DISABLE_RULE,
          text,
        });
      }
    }
  }
  return { sarif, results: out };
}

// このゲートが持つルールだけを判定対象にする。ESLint は登録されていないルールの
// disable コメント (web/src の react-hooks/exhaustive-deps など) を
// "Definition for rule ... was not found" として error で返すので、素通しにすると
// 正当なコメントを 1 行足しただけで複雑度 CI が落ちる。
const OWNED_RULES = new Set([
  "gocognit",
  "gocyclo",
  "funlen",
  "nestif",
  "dupl",
  "nolintlint",
  "complexity",
  "max-lines-per-function",
  "max-statements",
  "max-depth",
  "max-params",
  "max-nested-callbacks",
  "sonarjs/cognitive-complexity",
  "sonarjs/no-identical-functions",
  UNUSED_DISABLE_RULE,
]);

// 指標値はルールごとに位置が違うので、メッセージの「最初の数字」では拾えない。
// 実測: nestif の "`if len(nums) == 0` has complex nested blocks (complexity: 5)" は
// 条件式の 0 を先に拾ってしまい、5 -> 6 の悪化が消える。
//
// dupl と no-identical-functions はここに入れない — 数字がソース行番号なので、
// 値ではなく件数で比べる (measured が 0 のまま揃う)。
const POSITIONAL_RULES = new Set(["dupl", "sonarjs/no-identical-functions"]);
const VALUE_PATTERNS = {
  gocognit: /cognitive complexity (\d+)/,
  gocyclo: /cyclomatic complexity (\d+)/,
  funlen: /\((\d+) > \d+\)/,
  nestif: /\(complexity: (\d+)\)/,
  complexity: /complexity of (\d+)/,
  "max-lines-per-function": /too many lines \((\d+)\)/,
  "max-statements": /too many statements \((\d+)\)/,
  "max-depth": /nested too deeply \((\d+)\)/,
  "max-params": /too many parameters \((\d+)\)/,
  "max-nested-callbacks": /nested callbacks \((\d+)\)/,
  "sonarjs/cognitive-complexity": /Cognitive Complexity from (\d+)/,
};

// リネームされたファイルは merge base 側の旧パスへ寄せる。寄せないと `git mv` した
// だけで既存の違反が全部「この branch が持ち込んだ」に化ける。
const renames = new Map();
if (mergeBase) {
  try {
    const status = git(["diff", "--name-status", "-M", "--diff-filter=R", mergeBase]);
    for (const line of status.split("\n")) {
      const parts = line.split("\t");
      if (parts.length >= 3 && parts[0].startsWith("R")) renames.set(parts[2], parts[1]);
    }
  } catch {
    /* rename 検出に失敗しても現パスのまま比較する */
  }
}
const baseName = (file) => renames.get(file) ?? file;

// identity は「指標の数字」だけを伏せる。同じ関数が値を動かしても鍵は変わらず、
// 行番号のずれでも変わらない。
//
// 数字を全部潰さないのは、関数名や条件式の数字まで消えて Foo1 と Foo2 が同じ鍵に
// なるため。base の Foo2=30 が current の Foo1=25 を吸収して悪化を見逃す。
const METRIC_PATTERNS = [
  /complexity \d+/g,
  /\(complexity: \d+\)/g,
  /\(> \d+\)/g,
  /\(\d+ > \d+\)/g,
  /complexity of \d+/g,
  /lines \(\d+\)/g,
  /statements \(\d+\)/g,
  /deeply \(\d+\)/g,
  /parameters \(\d+\)/g,
  /callbacks \(\d+\)/g,
  /from \d+ to the \d+ allowed/g,
  /Maximum allowed is \d+/g,
];

// リネームはファイル側だけでなくメッセージ本文にも効かせる。dupl は相方のパスを
// 本文に書くので、片方を git mv すると相方の finding まで新規扱いになる。
const normalizeText = (rule, text) => {
  let out = text;
  for (const [after, before] of renames) out = out.split(after).join(before);
  // dupl 系のメッセージは数字が全部ソース行番号なので、まるごと伏せる。
  if (POSITIONAL_RULES.has(rule)) return out.replace(/\d+/g, "#");
  // nestif は先頭に条件式そのものを書く。条件を書き換えただけで複雑度が同じでも
  // 別 finding になってしまうので、条件は識別に使わない。
  if (rule === "nestif") out = out.replace(/^`[^`]*`/, "`#`");
  for (const pattern of METRIC_PATTERNS) {
    out = out.replace(pattern, (m) => m.replace(/\d+/g, "#"));
  }
  return out;
};
const identity = (r) => `${baseName(r.file)}|${r.rule}|${normalizeText(r.rule, r.text)}`;
const measured = (r) => {
  const pattern = VALUE_PATTERNS[r.rule];
  if (!pattern) return 0;
  const m = pattern.exec(r.text);
  return m ? Number(m[1]) : 0;
};

const rangeCache = new Map();

// changedRanges returns [start, end] pairs for lines this branch added or
// changed, or null when every line counts as new (untracked file).
function changedRanges(file) {
  if (rangeCache.has(file)) return rangeCache.get(file);
  let tracked = true;
  try {
    git(["ls-files", "--error-unmatch", "--", file]);
  } catch {
    tracked = false;
  }
  let ranges = null;
  if (tracked) {
    ranges = [];
    for (const line of git(["diff", "--unified=0", mergeBase, "--", file]).split("\n")) {
      // @@ -old,count +new,count @@ — the + side is the post-image.
      const m = /^@@ -\S+ \+(\d+)(?:,(\d+))? @@/.exec(line);
      if (!m) continue;
      const start = Number(m[1]);
      const count = m[2] === undefined ? 1 : Number(m[2]);
      if (count > 0) ranges.push([start, start + count - 1]);
    }
  }
  rangeCache.set(file, ranges);
  return ranges;
}

const owned = (r) => OWNED_RULES.has(r.rule);
const current = readResults(currentPath);
current.results = current.results.filter(owned);

// A finding survives when the baseline has no unconsumed entry that already
// covered it at an equal or higher value. Consuming greedily keeps duplicate
// anonymous functions ("Arrow function has too many statements") honest: two in
// the base absorb two now, a third one survives.
let survives;
if (basePath && fs.existsSync(basePath)) {
  const baseline = new Map();
  for (const r of readResults(basePath).results.filter(owned)) {
    const key = identity(r);
    if (!baseline.has(key)) baseline.set(key, []);
    baseline.get(key).push(measured(r));
  }
  // Ascending, so the match below consumes the SMALLEST baseline entry that
  // still covers the current value. Consuming the largest first would leave a
  // too-small entry for a later value and report it as new — dupl findings,
  // whose leading number is a line offset rather than a severity, hit this.
  for (const values of baseline.values()) values.sort((a, b) => a - b);
  survives = (r) => {
    const values = baseline.get(identity(r));
    if (!values || values.length === 0) return true;
    const value = measured(r);
    const i = values.findIndex((v) => v >= value);
    if (i < 0) return true;
    values.splice(i, 1);
    return false;
  };
} else if (mergeBase) {
  survives = (r) => {
    // Keep anything we cannot place: a dropped finding is a silent miss.
    if (!r.file || typeof r.line !== "number") return true;
    const ranges = changedRanges(r.file);
    if (ranges === null) return true;
    return ranges.some(([start, end]) => r.line >= start && r.line <= end);
  };
} else {
  survives = () => true;
}

const kept = new Set();
for (const r of current.results) {
  if (survives(r)) kept.add(r.raw);
}
for (const run of current.sarif.runs ?? []) {
  run.results = (run.results ?? []).filter((x) => kept.has(x));
}
// notification 由来の finding は SARIF の results に無いので、生き残ったものを
// results へ足して code scanning にも出す。
for (const r of current.results) {
  if (r.rule !== UNUSED_DISABLE_RULE || !kept.has(r.raw)) continue;
  const run = current.sarif.runs?.[0];
  if (!run) continue;
  run.results = run.results ?? [];
  run.results.push({
    ruleId: UNUSED_DISABLE_RULE,
    level: "warning",
    message: { text: r.text },
    locations: [
      { physicalLocation: { artifactLocation: { uri: r.file }, region: { startLine: r.line ?? 1 } } },
    ],
  });
}
fs.writeFileSync(currentPath, JSON.stringify(current.sarif));

for (const r of current.results) {
  if (kept.has(r.raw)) console.log(`${r.file}:${r.line ?? 0}: ${r.text} [${r.rule}]`);
}
