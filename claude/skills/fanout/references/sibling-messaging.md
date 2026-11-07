# Sibling messaging (`--team` / `fanout msg`)

Fanned panes are separate agent sessions, not Claude Code Agent Teams
teammates (a Claude-only, single-session feature). With `--team` they
coordinate through a per-parent SQLite message bus that works the same for
`claude`, `codex`, and `opencode` panes.

## Enabling

Pass `--team` to the fan-out like any other flag. The CLI adds a "Coordinating
with your sibling panes" section to each child's standard briefing and seeds the
created panes into the peer registry after the batch launches (best effort: a
registry failure never fails the fan-out). Codex Plan Mode children get the
minimal Plan briefing, so the section is skipped for them; they are still seeded
and can use `fanout msg`. In plan mode, when the plan includes Codex team tasks,
the registry preseed and each bridge's in-pane DB setup are fail-fast instead:
a bad `FANOUT_DB_PATH` or wrong DB ownership stops the run before pane creation
or fails that launch rather than falling back to pull.

Suggest `--team` when children will touch shared files (configs, schemas,
lockfiles) or have ordering dependencies not already encoded as blockers; skip
it for fully independent children.

## Verbs

`fanout msg` auto-detects which child you are (from the tmux pane and
`.fanout/state.json`) and which parent you belong to. Issue-mode peers are
addressed by issue number; `fanout plan --team` peers are addressed by task id
(`--to <task-id>`), because plan tasks have no `#N`.

| Verb | Effect |
|---|---|
| `fanout msg peers` | live sibling roster |
| `fanout msg inbox [--all] [--mark-read]` | unread 1:1 messages + unread board posts (`--mark-read` drains them) |
| `fanout msg board [--all]` | the shared broadcast board |
| `fanout msg watch [--interval S]` | block and emit new 1:1 + board messages one per line; emitted messages are marked read on delivery (mark-on-emit); Ctrl-C stops |
| `fanout msg send --to <N> [--kind K] "<body>"` | 1:1 message to sibling `N` |
| `fanout msg post [--kind K] "<body>"` | post to the shared board |
| `fanout msg nudge <N>` | best-effort push: an inbox hint through sibling `N`'s recorded runtime, only when its agent state can take queued input (never a blocked pane; opencode panes are excluded because they have no state refinement). Never touches the DB; undeliverable nudges warn and exit `0` |
| `fanout msg mark-read [--id N ...\|--all]` | mark 1:1 messages read (`--all` also advances the board cursor) |
| `fanout msg register` | (re-)register this pane in the roster |

Common options: `--json` (machine-readable), `--self <N>` / `--parent <ref>`
(override pane detection). `kind` is a free-form label (default `note`). Exit
codes: `0` ok, `2` bad invocation, `4` backend failure (SQLite, or `watch`'s
stdout breaking).

## Delivery model

Delivery is pull plus per-agent push lanes. Messages persist, and a sibling
reads them at its own checkpoints. On top of that, claude `--team` briefings
instruct the pane to start `fanout msg watch` under the Monitor tool as its
first tool action, so new messages stream in (marked read on emit), and fresh
non-Plan codex `--team` panes receive unread messages through an app-server
bridge as quoted turns. opencode panes have no push lane and stay pull-based.
Neither lane writes to pane input; `nudge` is the only push that does.

A watcher replaces only the inbox checks. Post a one-line heads-up before
editing shared files regardless. Without a watcher (no Monitor, a restored
pane), check `fanout msg inbox` once after reading the briefing and once more
before opening the PR. A nudge hint can land after the recipient's watcher
already drained the message; an empty inbox then means the body is in the
watcher output.

## Security

The DB is a plaintext SQLite file under `/tmp` (`0600`, owner-only). Keep
secrets, tokens, and credentials out of messages.
