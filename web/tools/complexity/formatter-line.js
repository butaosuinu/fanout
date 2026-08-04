// 1 問題 1 行の ESLint フォーマッタ。`file:line:col: message [rule]`。
//
// ESLint 10 は core から unix / compact フォーマッタを外したため、行指向の出力を
// 得る手段が無い。hook (scripts/agent-complexity-on-edit.sh) は変更行だけを残す
// フィルタを awk でかけるので、JSON ではなく 1 行 1 件が要る — hook は jq も node も
// 使わない規約なので、整形はここで済ませる。
import path from "node:path";

export default function formatLines(results) {
  const lines = [];
  for (const result of results) {
    // Relative to cwd (web/) so findings read like the Go ones and stay short in
    // the agent's context. The hook re-attaches the web/ prefix.
    const file = path.relative(process.cwd(), result.filePath);
    for (const message of result.messages) {
      const rule = message.ruleId ?? "eslint";
      lines.push(`${file}:${message.line}:${message.column}: ${message.message} [${rule}]`);
    }
  }
  return lines.length > 0 ? `${lines.join("\n")}\n` : "";
}
