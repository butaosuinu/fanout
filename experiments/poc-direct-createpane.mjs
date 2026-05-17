#!/usr/bin/env node
//
// PoC: Drive dmux pane creation by importing dist/utils/paneCreation.js
// directly, bypassing the tmux-popup intercept fanout currently uses.
//
// THIS SCRIPT HAS SIDE EFFECTS: when run inside a live dmux session it will
// create a real worktree, a real git branch, and a real tmux pane. Run only
// against a throwaway repo. Do NOT run from CI.
//
// Why we're trying this:
//   The fanout CLI today drives dmux's new-pane flow by intercepting the two
//   `tmux display-popup` result files (newPanePopup, agentChoicePopup). That
//   contract is fragile — it depends on internal popup script paths, the
//   PopupWrapper JSON shape, and pgrep matching the right Node process. If
//   we can call dmux's `createPane(options, availableAgents)` directly, we
//   skip:
//     - the popup PID hunt
//     - the wait_for_new_pane panes[].length polling
//     - the LLM slug call (by passing `slugBase`)
//     - the agent-picker popup intercept (by passing `agent` + `skipAgentSelection`)
//   in exchange for binding to a private dmux internal API that has no semver
//   guarantee. This script is the smallest possible end-to-end test.
//
// Prerequisites:
//   - dmux is running in tmux (cd <repo> && dmux), and you're attached to
//     that tmux session
//   - $TMUX is set (this process inherits the session via the tmux env)
//   - dmux v5.8.1 installed at the path below; bump if you upgrade
//
// Usage:
//   node experiments/poc-direct-createpane.mjs <project_root> <prompt> <agent> [slugBase] [branchName]
//
// Example:
//   cd /tmp && git init poc-target && cd poc-target && \
//     git commit --allow-empty -m initial && dmux
//   # in another pane of the SAME tmux session:
//   node /Users/butaosuinu/fanout/experiments/poc-direct-createpane.mjs \
//       /tmp/poc-target "poc test prompt" claude poc-slug feat/poc-branch
//
// What to look for in the output:
//   - `{ pane: { id, slug, branchName, ... }, needsAgentChoice: false }`
//     → success: dmux returned a pane object without asking for agent choice
//   - `needsAgentChoice: true` → agent name wasn't accepted; check
//     availableAgents in dmux settings
//   - thrown errors → likely TmuxService misdetection (not inside a tmux
//     session?), worktree path collision, or invalid branch name
//
// Checks to perform after success:
//   1. `.dmux/dmux.config.json` panes[] should contain the new pane
//   2. `.dmux/worktrees/<slug>` directory exists, git branch matches
//      <branchName> (or branchPrefix + <slug> if branchName wasn't supplied)
//   3. dmux did NOT pop up any popup during creation
//   4. dmux's slug LLM did NOT fire (no OpenRouter / claude --no-interactive
//      child process) — verify with `ps auxf | grep claude` while running

import { existsSync } from 'node:fs';
import path from 'node:path';

// --- pin dmux path so accidental version drift fails loudly ----------------
const DMUX_VERSION_EXPECTED = '5.8.1';
const DMUX_ROOT = '/Users/butaosuinu/.nodebrew/node/v24.13.0/lib/node_modules/dmux';
const dmuxPkg = JSON.parse(
  await import('node:fs').then(({ readFileSync }) =>
    readFileSync(path.join(DMUX_ROOT, 'package.json'), 'utf-8')
  )
);
if (dmuxPkg.version !== DMUX_VERSION_EXPECTED) {
  console.error(
    `[poc] dmux ${dmuxPkg.version} found at ${DMUX_ROOT}; this PoC was written ` +
    `against ${DMUX_VERSION_EXPECTED}. Re-verify the createPane signature and ` +
    `paneActions registry before trusting this run.`
  );
  process.exit(2);
}

// --- args ------------------------------------------------------------------
const [, , projectRoot, prompt, agent, slugBase = '', branchName = ''] = process.argv;
if (!projectRoot || !prompt || !agent) {
  console.error(
    'usage: node poc-direct-createpane.mjs <project_root> <prompt> <agent> [slugBase] [branchName]'
  );
  process.exit(2);
}
if (!existsSync(path.join(projectRoot, '.dmux', 'dmux.config.json'))) {
  console.error(
    `[poc] ${projectRoot}/.dmux/dmux.config.json not found. Is dmux running ` +
    `there? \`cd ${projectRoot} && dmux\` first.`
  );
  process.exit(2);
}
if (!process.env.TMUX) {
  console.error(
    '[poc] $TMUX is not set. This script must run inside the same tmux ' +
    'session as dmux so TmuxService.getCurrentPaneIdSync() can resolve.'
  );
  process.exit(2);
}

// --- import dmux internals -------------------------------------------------
// These imports are *private* to dmux. They have no semver guarantee; if dmux
// renames or restructures these files, the PoC breaks loudly here.
const { createPane } = await import(`${DMUX_ROOT}/dist/utils/paneCreation.js`);

// --- invoke ----------------------------------------------------------------
console.log('[poc] calling createPane with:', {
  projectRoot,
  prompt,
  agent,
  slugBase: slugBase || '(none → will run slug LLM)',
  branchNameOverride: branchName || '(none → branchPrefix + slug)',
});

try {
  const result = await createPane(
    {
      prompt,
      agent,
      projectRoot,
      existingPanes: [],
      slugBase: slugBase || undefined,
      branchNameOverride: branchName || undefined,
      skipAgentSelection: true,
      sessionConfigPath: path.join(projectRoot, '.dmux', 'dmux.config.json'),
      sessionProjectRoot: projectRoot,
    },
    [agent]
  );
  console.log('[poc] createPane returned:');
  console.log(JSON.stringify(result, null, 2));
  if (result?.needsAgentChoice) {
    console.error(
      '[poc] WARNING: needsAgentChoice=true means dmux did not accept the ' +
      `agent name "${agent}". Check that it is in availableAgents for this ` +
      'project (dmux settings / kebab menu).'
    );
    process.exit(3);
  }
} catch (err) {
  console.error('[poc] createPane threw:');
  console.error(err?.stack || err);
  process.exit(1);
}
