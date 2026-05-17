# PoC: Direct `createPane()` invocation (dmux v5.8.1)

## Goal

Determine whether fanout could replace its popup-intercept strategy by
importing and calling dmux's internal `createPane(options, availableAgents)`
function from `dist/utils/paneCreation.js`. If feasible, this would let us:

- skip the popup PID hunt and the result-file write
- skip `wait_for_new_pane` polling (`createPane` returns the pane object)
- pass `slugBase` directly, eliminating the LLM slug call
- pass `agent` + `skipAgentSelection: true`, eliminating the agent-picker popup

…at the cost of binding to private dmux internals with no semver guarantee.

## Files

- `poc-direct-createpane.mjs` — the PoC entry point. Pins the dmux version
  (refuses to run on any version other than 5.8.1) and calls `createPane()`
  with the minimum viable options.

## Static analysis (without running)

Read against `/Users/butaosuinu/.nodebrew/node/v24.13.0/lib/node_modules/dmux/dist/utils/paneCreation.js`:

| Concern | Finding | Implication |
|---|---|---|
| Function exists and is exported | `export async function createPane(options, availableAgents)` at `paneCreation.js:82` | ✅ Importable from outside dmux |
| Returns `{pane, needsAgentChoice}` | Yes (lines 137-140, 423-426) | ✅ Direct synchronous result |
| `slugBase` option | `(slugBase \|\| await generateSlug(prompt))` at `paneCreation.js:166-168` | ✅ LLM slug call skipped when supplied |
| `branchNameOverride` option | Validated `paneCreation.js:158-161`, then passed to `resolvePaneNaming` | ✅ Branch name fully controllable |
| `skipAgentSelection: true` + explicit `agent` | Branch `paneCreation.js:130-133` skipped when `agent` is set | ✅ Agent picker bypassed |
| `existingPanes: []` accepted | Used at `paneCreation.js:243` (`isFirstContentPane = existingPanes.length === 0`) | ⚠️ Treating the new pane as the **first** content pane triggers `setupSidebarLayout(controlPaneId, projectRoot)` instead of `splitPane`. When fanning out into an already-populated dmux session, this branch would create the wrong layout. fanout needs to pass the real `panes[]` from `dmux.config.json` |
| `sessionConfigPath` + `sessionProjectRoot` | Both supported (options destructure at `paneCreation.js:84-85`) | ✅ Can run without `git rev-parse` autodetect |
| `controlPaneId` resolved from config | Loaded from `<projectRoot>/.dmux/dmux.config.json`, self-heals if stale (`paneCreation.js:194-237`) | ✅ Works as long as config file exists |
| Hard tmux dependencies | `TmuxService.getInstance()` (singleton), `getCurrentPaneIdSync()`, `setupSidebarLayout`/`splitPane`, `recalculateAndApplyLayout`, `tmuxService.refreshClient()` | ⚠️ **Must run inside the same tmux session as dmux**. From a node process outside tmux, `getCurrentPaneIdSync()` returns garbage and pane splits fail |
| `StateManager.getInstance()` | Per-process singleton, starts empty in the PoC. `state.serverPort` is checked at `paneCreation.js:351` but only used to pass `DMUX_SERVER_PORT` into hooks. Undefined is the normal case anyway | ✅ Safe to leave empty |
| `before_pane_create` hook | Officially fired (`paneCreation.js:146-150`). Our existing `.dmux-hooks/before_pane_create` would run | ✅ No new hook plumbing required |
| `pane_created` / `worktree_created` hooks | Fired at `paneCreation.js:319-325` and inside the bootstrap runner | ✅ Same hook surface as the popup path |
| `displayName` from `existingWorktreeMetadata` | `paneCreation.js:332` — only read from existing metadata, not settable per-call. fanout's `apply_display_names` post-write would still be needed | ⚠️ Display-name plumbing unchanged |
| dmux semver | dist/ is private. The function signature changed shape between v5.6.3 and v5.8.1 (added `slugBase`, `branchNameOverride`, `skipAgentSelection`); will likely keep evolving | ⚠️ Smoke-test the PoC against every dmux bump |

## What the PoC does NOT verify (live-only)

The script does not auto-run because it has real side effects (creates a git
worktree and a tmux pane). Run it manually against a throwaway repo to verify:

1. **TmuxService prereqs**: confirm `$TMUX` is honored — that the PoC actually
   sees the same session as the dmux instance, not some other tmux server.
2. **`existingPanes` correctness**: pass the real `panes[]` from
   `dmux.config.json` (not `[]`) and confirm `splitPane` is taken, not
   `setupSidebarLayout`. Without this, multi-pane fanouts will mangle layout.
3. **Config concurrency**: if fanout calls `createPane()` in a loop, two calls
   may race on `atomicWriteJsonSync(configPath, config)`. dmux's TUI batches
   these — we don't. Verify by firing 3 PoC calls back-to-back and inspecting
   `panes[]` for missing entries.
4. **`needsAgentChoice` semantics**: confirm that passing
   `skipAgentSelection: true` + an unknown agent name throws (or returns
   `{pane: null, needsAgentChoice: false}`) rather than silently creating a
   broken pane.
5. **Slug LLM truly skipped**: while the PoC runs, run `ps auxf | grep -E
   'openrouter|claude --no-interactive'` and confirm no child process appears
   when `slugBase` is supplied.
6. **dashboard sync**: dmux's running TUI maintains its own in-memory panes
   list and rewrites `dmux.config.json` from it on the next event. After the
   PoC pane is added, confirm dmux's `usePanes.loadPanes` poll (5-30s) picks
   it up and doesn't silently drop it.

## Risks vs benefits

**Benefits of switching:**
- ~80 lines of fragile `pgrep` / `ps` / popup-process-tree handling deleted from `fanout`
- Slug becomes deterministic without depending on LLM echo-back of the slug-hint
- ~0.5-1s saved per pane (no popup mount + no LLM call when `slugBase` set)
- Cleaner error handling (exceptions vs "popup didn't appear within Ns" timeouts)

**Risks of switching:**
- Private API: any dmux refactor (rename, options reshape, `StateManager`
  change) silently breaks the PoC. The popup-intercept contract has held
  v5.6.3 → v5.8.1; the `createPane` signature has already moved once.
- We become a Node-only path. The current fanout is a Bash script that
  spawns short-lived processes; this PoC requires either rewriting fanout
  in Node, or spawning Node from Bash and parsing its stdout, or wrapping
  the createPane call behind a tiny long-running daemon (yuck).
- Concurrency: dmux's TUI serializes pane creation via its event loop.
  Calling `createPane` from outside that loop can race the TUI's
  `savePanesToFile`.
- No HTTP boundary: if anything goes wrong (worktree corruption, tmux state
  drift) the PoC and dmux share the same process-local invariants. The
  popup intercept at least isolates failures behind a process boundary.

## Recommendation

**Defer the migration.** The popup-intercept contract has survived two minor
dmux bumps (5.6.3 → 5.8.1) without breaking, while the internal `createPane`
options shape has already changed in the same window. The savings (~1s/pane,
80 lines of Bash) are real but small relative to the ongoing maintenance
burden of tracking a private Node API from a Bash codebase.

When dmux ships its documented HTTP API (`POST /api/panes`, currently a
skeleton at `dist/adapters/apiActionHandler.js` with no transport), revisit
the migration there — that path *is* semver-stable.

In the meantime, the **immediate value** delivered to fanout via Track B+C
(passing `branchName` through the popup payload) captures most of the
deterministic-naming benefit without binding to private internals.

## How to run the PoC yourself

```bash
# 1. Disposable target repo
mkdir -p /tmp/dmux-poc && cd /tmp/dmux-poc
git init -q && git commit --allow-empty -m initial -q

# 2. Start dmux in the same shell (in tmux)
dmux
# In dmux's TUI: let it bootstrap, then keep the session attached.

# 3. From another pane of the SAME tmux session:
node /Users/butaosuinu/fanout/experiments/poc-direct-createpane.mjs \
    /tmp/dmux-poc "poc test prompt" claude poc-slug feat/poc-branch

# 4. After the call returns, check:
ls /tmp/dmux-poc/.dmux/worktrees/                         # should show poc-slug/
git -C /tmp/dmux-poc/.dmux/worktrees/poc-slug branch --show-current
                                                          # should print feat/poc-branch
jq '.panes[] | {slug,branchName,prompt}' /tmp/dmux-poc/.dmux/dmux.config.json

# 5. Clean up
rm -rf /tmp/dmux-poc
tmux kill-session -t <the-dmux-session>
```

## If you keep this around

`experiments/` is intentionally outside the test suite and the install path.
Either:
- Add `experiments/` to `.gitignore` (recommended — the PoC is reference
  material, not a shipped artifact).
- Or commit it as documented `experiments/` work, with a top-level README
  warning that nothing in here is run by `make test` and that the version
  pins inside `poc-direct-createpane.mjs` must be bumped manually whenever
  dmux is upgraded.
