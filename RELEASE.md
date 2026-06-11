# Releasing fanout

Cutting a release is **CI-driven**: the only manual step is pushing an annotated
`vX.Y.Z` tag. Pushing the tag triggers `.github/workflows/release.yml`, which
builds the four platform archives, generates `SHA256SUMS`, and publishes the
GitHub Release with auto-generated notes. There is no `make release` target and
no goreleaser config — the workflow is the whole pipeline.

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

2. **Preflight.** Release off the latest `origin/main` and make sure it is green:

   ```bash
   gh run list --branch main --workflow test.yml --limit 1 \
     --json headSha,conclusion          # conclusion == "success" on main's tip
   make test && make lint && make vuln   # optional local re-check (vuln needs network)
   git tag -l vX.Y.Z                     # must be empty — tag not yet used
   ```

3. **Tag and push.** Use an **annotated** tag pointing at `origin/main`, matching
   how every prior tag was cut:

   ```bash
   git tag -a vX.Y.Z origin/main -m "vX.Y.Z"
   git push origin vX.Y.Z                 # this starts the public release
   ```

   The tag does not have to be created from the `main` branch checkout —
   `origin/main` is named explicitly, so any clean worktree works. Note the PR
   review gate (`.claude/hooks/pre-pr-review-gate.sh`) only intercepts
   `gh pr create`; **a tag push is not gated.**

4. **Watch the build.**

   ```bash
   gh run watch "$(gh run list --workflow release.yml --limit 1 \
     --json databaseId -q '.[0].databaseId')" --exit-status
   ```

5. **Verify the Release.**

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
  instead.
