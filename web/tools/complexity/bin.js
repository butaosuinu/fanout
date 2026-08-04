#!/usr/bin/env node
// complexity-lint — runs the ESLint that lives in this package's own node_modules.
//
// The eslint binary is not reachable from web/node_modules/.bin: this package holds
// its own dependency tree so that typescript@6 stays isolated from web/'s typescript@7.
// Exposing this shim as a bin is what makes `pnpm exec complexity-lint` work from web/.
// Arguments pass straight through; cwd is inherited so ESLint resolves web/eslint.config.js
// and its src/** patterns against web/.
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const eslint = path.join(here, "node_modules", ".bin", "eslint");

const result = spawnSync(eslint, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`complexity-lint: ${eslint} を起動できません: ${result.error.message}`);
  process.exit(2);
}
process.exit(result.status ?? 2);
