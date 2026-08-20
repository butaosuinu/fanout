# Releasing fanout

Publishing a release is **CI-driven**: after the release docs land on `main`,
the only manual publication step is pushing an annotated `vX.Y.Z` tag. Pushing
the tag triggers `.github/workflows/release.yml`, which builds the four platform
archives, generates `SHA256SUMS`, and publishes the GitHub Release with
auto-generated notes. The same tag also triggers `.github/workflows/pages.yml`,
which publishes the docs site as of that tag — merging the release docs to
`main` builds them but does not publish them. There is no `make release` target
and no goreleaser config — the workflow is the whole pipeline.

## What the tag push does for you

`release.yml` fires on `push` of any `v*` tag and:

1. Builds `darwin`/`linux` × `amd64`/`arm64` with
   `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<tag> -X main.commit=<sha7>"`.
   The version string is **injected from the tag name** (`GITHUB_REF_NAME`) — see
   `cmd/fanout/main.go` (`version`/`commit` default to `dev`/`none` for local
   builds). **No source edit is needed to bump the version.**
2. Smoke-tests `./dist/fanout --version` on the native arch of each runner.
3. Packages each binary together with `claude/`, `codex/`, and `LICENSE` into
   `fanout_<os>_<arch>.tar.gz`.
4. Generates `SHA256SUMS` and runs
   `gh release create "<tag>" --title "<tag>" --generate-notes <archives> SHA256SUMS`.
   The notes are built from the merged PRs since the previous tag.

`install.sh` then serves these assets to users (latest Release, or a pinned
`FANOUT_VERSION`).

## Steps

1. **Pick the version.** Look at the latest tag and the PRs merged since it:

   ```bash
   git fetch origin main --tags
   git describe --tags --abbrev=0            # latest tag, e.g. v0.2.0
   git log --oneline "$(git describe --tags --abbrev=0)"..origin/main
   ```

   Bump per semver (`v0.MINOR.PATCH` while pre-1.0: features bump MINOR, fixes
   bump PATCH).

2. **Prepare the release docs.** Add the release date and highlights to
   `site/content/docs/changelog.{md,ja.md}`, then update the pinned
   `FANOUT_VERSION` example in `site/content/docs/installation.{md,ja.md}`.
   Merge the docs through a normal PR; its squash commit becomes the intended
   tag target. No source version edit is needed. Merging does not publish the
   site — the tag does.

3. **Preflight.** Release off the latest `origin/main` and make sure it is green:

   ```bash
   git fetch origin main --tags
   gh run list --branch main --workflow test.yml --limit 1 \
     --json headSha,conclusion          # conclusion == "success" on main's tip
   gh run list --branch main --workflow vuln.yml --limit 1 \
     --json headSha,conclusion          # conclusion == "success" on the same tip
   make check                            # optional deterministic local re-check
   make vuln                             # optional; needs the network-backed vuln DB
   git tag -l vX.Y.Z                     # must be empty — tag not yet used
   ```

4. **Tag and push.** Use an **annotated** tag pointing at `origin/main`, matching
   how every prior tag was cut:

   ```bash
   git tag -a vX.Y.Z origin/main -m "vX.Y.Z"
   ```

   ```bash
   git push origin vX.Y.Z                 # this starts the public release
   ```

   Run the two commands as separate calls: the agent push gate denies a push
   chained after `git tag` in the same call (the tag does not exist yet when
   the hook inspects the command, so its tag exemption cannot apply). The tag
   does not have to be created from the `main` branch checkout —
   `origin/main` is named explicitly, so any clean worktree works. Note the
   PR review gate (`.claude/hooks/pre-pr-review-gate.sh`) only intercepts
   `gh pr create`; **a tag push on its own is not gated.**

5. **Watch the build.** The tag starts `release.yml` and `pages.yml`. Deleting a
   tag does not delete the runs it started, so a re-cut tag has older runs under
   the same name — match on the tagged commit. Stop waiting after a minute:
   whether a run exists is the thing you are trying to find out.

   ```bash
   tag_sha="$(git rev-parse "vX.Y.Z^{commit}")"
   export tag_sha
   watch_run() {
     local id=""
     for _ in $(seq 12); do
       id="$(gh run list --workflow "$1" --branch vX.Y.Z --limit 10 \
         --json databaseId,headSha -q '[.[] | select(.headSha == $ENV.tag_sha)][0].databaseId')" ||
         { echo "lookup failed for $1"; return 2; }
       [ -n "$id" ] && break
       sleep 5
     done
     [ -n "$id" ] || { echo "no $1 run for $tag_sha"; return 3; }
     gh run watch "$id" --exit-status
   }
   ```

   ```bash
   watch_run release.yml
   watch_run pages.yml
   ```

   Only a `3` from `watch_run pages.yml` means the run was never created, and
   only then is publishing by hand right. `2` is a failed lookup — expired
   auth, a rate limit, no network — and `1` is `gh run watch` reporting a run
   that exists and failed, or losing the connection to one. Publishing on
   either reading can start a second deploy on top of one already running.

   If `pages.yml` never produced a run, publish the tag by hand:

   ```bash
   gh workflow run pages.yml --ref vX.Y.Z
   ```

   Name the tag, not `main` — `--ref main` would publish whatever else has
   landed on `main` since, including release docs staged for the *next* tag.

6. **Verify the Release.**

   ```bash
   gh release view vX.Y.Z
   ```

   Confirm: the four `fanout_{darwin,linux}_{amd64,arm64}.tar.gz` archives plus
   `SHA256SUMS` are attached, the notes list the expected PRs with a
   `Full Changelog: <prev>...vX.Y.Z` link, and it is flagged `Latest`. As a final
   end-to-end check, install the published artifact:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh \
     | FANOUT_VERSION=vX.Y.Z sh -s -- --no-skills
   fanout --version            # -> fanout vX.Y.Z (<sha7>)
   ```

   Then confirm the docs site carries the new version. `pages.yml` and
   `release.yml` run in parallel, so the changelog's release-notes link 404s
   until the Release is published a few minutes in:

   ```bash
   curl -fsS https://butaosuinu.github.io/fanout/docs/changelog/ | grep -qF 'vX.Y.Z'
   ```

   Pages serves through a CDN, so a miss right after the deploy job goes green
   can be propagation rather than a failed publish. Wait a minute and re-run
   before treating it as one.

## If something goes wrong

- **Workflow failed mid-build.** Fix the cause on `main`, then delete and re-cut
  the tag (the Release is only created in the final job, so a failed build leaves
  no partial Release):

  ```bash
  git push origin :refs/tags/vX.Y.Z      # delete remote tag
  git tag -d vX.Y.Z                      # delete local tag
  ```

  Re-tag once `main` is fixed and green.

- **Wrong commit tagged.** Same delete dance, then re-tag the intended commit.
  Avoid moving a tag that has already published a Release; bump to the next patch
  instead. If the tagged commit was not `main`'s tip, the docs site rolls back
  to it; re-cutting the tag republishes the site along with the Release.

- **Docs deploy rejected.** `not allowed to deploy to github-pages due to
  environment protection rules` means the `github-pages` environment lost its tag
  policy. Restore it, then re-run the failed job:

  ```bash
  policies=repos/butaosuinu/fanout/environments/github-pages/deployment-branch-policies
  gh api --method POST "$policies" -f name='main' -f type='branch'
  gh api --method POST "$policies" -f name='v*' -f type='tag'
  ```

  Both entries are needed: the tag publishes releases, `main` backs every
  `gh workflow run pages.yml --ref main`. The POST only works while the
  environment is set to *selected branches and tags*; if it was reset to
  another mode, switch it back in Settings → Environments → github-pages
  first. The Release itself is unaffected — `pages.yml` and `release.yml` fail
  independently.
