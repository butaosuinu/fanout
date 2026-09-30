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
//   node complexity-diff.mjs --current <sarif> [--base <sarif>] [--base-root <dir>] [--merge-base <sha>] [--root <dir>]
//
// --base があれば回帰比較、無ければ --merge-base からの変更行フィルタに退避する。
// 生き残った finding を stdout へ 1 行 1 件で出し、絞り込み後の SARIF を --current へ
// 書き戻す。呼び出し側は行数を数えて判定する。
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
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
// ベースラインを測った木 (merge base の展開先)。funlen の receiver と init / _ の宣言を読むのに使う。
const baseRoot = opt("--base-root");

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

// tree は SARIF を測ったソースの木。宣言行を読み直すときの基点になる。
function readResults(file, tree) {
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
        tree,
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

// 鍵 (keys) は「指標の数字」だけを伏せる。同じ関数が値を動かしても鍵は変わらず、
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
// Go の関数単位ルールは場所をファイルでなくパッケージ (ディレクトリ) で鍵にする。
// Go の関数名はパッケージ内で一意で、メソッドは gocognit/gocyclo の本文に receiver
// が載るので、ディレクトリ + 本文で同じ関数を指せる。ファイルで鍵にすると、同じ
// パッケージ内で既存の関数を別ファイルへ移しただけで新規違反に化ける。
//
// 例外が 2 つある。
// - 関数の init と _ はパッケージ内に何個でも宣言できるので名前では区別できない。パッケージで
//   鍵にすると、ある init の base 値が別ファイルの新しい init を吸収する。基本はファイル単位で、
//   --base-root があれば宣言本文の指紋でもパッケージ内を突き合わせる (keys を参照)。
//   receiver 付きの init メソッドは一意なので例外にしない。
// - funlen の本文は receiver を書かない ("Function 'Run' is too long")。測った木の
//   宣言行から receiver を読んで鍵に足し、(*A).Run が (*B).Run を吸収しないようにする。
//   読めなければファイル単位へ退避する (取りこぼすより新規扱いのほうがまし)。
//   --base-root が無い呼び出し (編集フック) は base 側の receiver を読めないので、
//   両側ともファイル単位にそろえる。片側だけ receiver 付きの鍵にすると一致しない。
const PACKAGE_KEYED_RULES = new Set(["gocognit", "gocyclo", "funlen"]);
const REPEATABLE_FUNCS = new Set(["init", "_"]);
const funcName = (r) => {
  const m = /func `([^`]*)`|Function '([^']*)'/.exec(r.text);
  return m ? (m[1] ?? m[2]) : undefined;
};
const sourceCache = new Map();
const sourceLines = (file) => {
  if (!sourceCache.has(file)) {
    let lines = null;
    try {
      lines = fs.readFileSync(file, "utf8").split("\n");
    } catch {
      /* 読めなければ receiver 不明として扱う */
    }
    sourceCache.set(file, lines);
  }
  return sourceCache.get(file);
};
// Go の識別子は Unicode の文字・数字を含む。\w だと (*A日) と (*A月) がどちらも A になる。
const GO_IDENT = String.raw`[\p{L}_][\p{L}\p{Nd}_]*`;
const FUNC_DECL = new RegExp(String.raw`^func\s+${GO_IDENT}\s*[[(]`, "u");
// 型名は "*" と "(" を読み飛ばした先の識別子 ((a *(A)) / ((A)) / (a (*A)) も gofmt が残す)。
// 型名の直後が receiver の終わり (")" / 複数行の "," / 型引数の "[") でなければ読まない。
// (a *(A)) で変数名 a を型名と取り違えないため。
const METHOD_DECL = new RegExp(
  String.raw`^func\s*\(\s*(?:${GO_IDENT}\s+)?[\s*(]*(${GO_IDENT})(?=\s*[),[])`,
  "u",
);
// gofmt は func と receiver の間のコメントも残すので、読む前に落とす。複数行コメントの
// 本文は行頭から始まりうるので、改行を残して空白にする。
const blankComments = (s) =>
  s.replace(/\/\*[\s\S]*?\*\/|\/\/[^\n]*/g, (c) => c.replace(/[^\n]/g, " "));
// declText は end 行で終わる宣言を func 行から読み、コメントを空白にして返す。
// コメント中の行頭 "func" を拾うと閉じ "*/" だけが残るので、さらに前の func 行を探す。
const declText = (lines, end) => {
  for (let stop = end; ; ) {
    const start = lines.slice(0, stop).findLastIndex((l) => /^func\b/.test(l));
    if (start < 0) return null;
    const decl = blankComments(lines.slice(start, end).join("\n"));
    if (!decl.includes("*/")) return decl;
    stop = start;
  }
};
// receiverOf は報告行を含む宣言の receiver 型名を返す。関数なら ""、判定できなければ
// null。receiver が複数行にまたがると funlen はメソッド名の行 (") Run() {") を報告する
// ので、func 行まで遡ってから receiver を読む。
const receiverOf = (r) => {
  if (!r.tree || typeof r.line !== "number") return null;
  const lines = sourceLines(path.resolve(r.tree, r.file));
  if (!lines || r.line < 1 || r.line > lines.length) return null;
  const decl = declText(lines, r.line);
  if (decl === null) return null;
  // receiver の途中の行は字下げか ")" で始まる。別の宣言をまたいだら諦める。
  if (decl.split("\n").slice(1, -1).some((l) => /^[^\s)]/.test(l))) return null;
  // 関数と確かに読めたときだけ ""。どちらとも読めなければ null でファイル単位へ退避する。
  if (FUNC_DECL.test(decl)) return "";
  const m = METHOD_DECL.exec(decl);
  return m ? m[1] : null;
};
const location = (r) => {
  const file = baseName(r.file);
  if (!file.endsWith(".go") || !PACKAGE_KEYED_RULES.has(r.rule)) return file;
  // gocognit/gocyclo はメソッドを "(*A).init" と書くので、ここで当たるのは関数だけ。
  if (r.rule !== "funlen") return REPEATABLE_FUNCS.has(funcName(r)) ? file : path.dirname(file);
  // funlen はメソッドでも "Function 'init'" と書くので、receiver が無いときだけ例外にする。
  const recv = baseRoot ? receiverOf(r) : null;
  if (recv === null || (recv === "" && REPEATABLE_FUNCS.has(funcName(r)))) return file;
  return `${path.dirname(file)}|${recv}`;
};
// fingerprintOf は報告行の func 宣言をコメント抜き・空白詰めで読み、そのハッシュを返す。
// gofmt 済みのトップレベル関数は行頭の "}" で閉じる (1 行の関数は func 行で閉じる)。
// 報告行が func 行でない・閉じが見つからないときは null。
// ponytail: 行頭に "}" を書いた raw string があるとそこで切れる。切れた先の編集は指紋に
// 出ないが、値の比較は残る。字句解析が要るほどの差ではない。
const fingerprintOf = (r) => {
  if (!r.tree || typeof r.line !== "number") return null;
  const lines = sourceLines(path.resolve(r.tree, r.file));
  if (!lines || r.line < 1 || r.line > lines.length) return null;
  const start = r.line - 1;
  if (!/^func\b/.test(lines[start])) return null;
  const end = /\}\s*$/.test(blankComments(lines[start]))
    ? start
    : lines.findIndex((l, i) => i > start && /^\}/.test(l));
  if (end < 0) return null;
  const decl = blankComments(lines.slice(start, end + 1).join("\n")).replace(/\s+/g, " ").trim();
  return createHash("sha256").update(decl).digest("hex");
};
// receiver の無い init / _ か。funlen はメソッドでも同じ本文なので宣言を読む。
const isRepeatableFunc = (r) =>
  r.file.endsWith(".go") &&
  PACKAGE_KEYED_RULES.has(r.rule) &&
  REPEATABLE_FUNCS.has(funcName(r)) &&
  (r.rule !== "funlen" || receiverOf(r) === "");
// keys は突き合わせる鍵を優先順に返す。init / _ は測った木が両側読めるとき (--base-root あり)
// パッケージ + 宣言の指紋の鍵を先に試し、当たらなければファイル単位の鍵へ落ちる。
// ファイル単位を残すのは、既存の init を 1 行直しただけで新規扱いにしないため
// (指紋は中身が変われば変わる)。宣言を読めなければファイル単位だけ。
const keys = (r) => {
  const text = `${r.rule}|${normalizeText(r.rule, r.text)}`;
  const byLocation = `${location(r)}|${text}`;
  if (!baseRoot || !isRepeatableFunc(r)) return [byLocation];
  const fp = fingerprintOf(r);
  return fp === null ? [byLocation] : [`${path.dirname(baseName(r.file))}|${text}|${fp}`, byLocation];
};
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
const current = readResults(currentPath, root);
current.results = current.results.filter(owned);

// A finding survives when the baseline has no unconsumed entry that already
// covered it at an equal or higher value. Consuming greedily keeps duplicate
// anonymous functions ("Arrow function has too many statements") honest: two in
// the base absorb two now, a third one survives.
//
// A baseline entry is registered under every key it has and consumed once. Keys
// are tried pass by pass over ALL current findings, so a moved init claims its
// fingerprint entry before an edited init elsewhere can take it by file key.
let survives;
if (basePath && fs.existsSync(basePath)) {
  const baseline = new Map();
  for (const r of readResults(basePath, baseRoot).results.filter(owned)) {
    const entry = { value: measured(r), used: false };
    for (const key of keys(r)) {
      if (!baseline.has(key)) baseline.set(key, []);
      baseline.get(key).push(entry);
    }
  }
  // Ascending, so the match below consumes the SMALLEST baseline entry that
  // still covers the current value. Consuming the largest first would leave a
  // too-small entry for a later value and report it as new — dupl findings,
  // whose leading number is a line offset rather than a severity, hit this.
  for (const entries of baseline.values()) entries.sort((a, b) => a.value - b.value);
  const matched = new Set();
  const pending = current.results.map((r) => ({ r, keys: keys(r), value: measured(r) }));
  for (let pass = 0; pass < 2; pass++) {
    for (const p of pending) {
      if (matched.has(p.r.raw) || pass >= p.keys.length) continue;
      const entry = baseline.get(p.keys[pass])?.find((e) => !e.used && e.value >= p.value);
      if (!entry) continue;
      entry.used = true;
      matched.add(p.r.raw);
    }
  }
  survives = (r) => !matched.has(r.raw);
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
