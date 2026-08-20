# fanout docs site

Hugo (Extended) site published to <https://butaosuinu.github.io/fanout/>.
Custom theme **PAPER BREEZE** (no external theme/module); bilingual via
`*.md` (en) / `*.ja.md` (ja). Built and deployed by
`.github/workflows/pages.yml`.

## Publishing

Merging to `main` builds the site but does not publish it. The live site changes
on two events only:

- a `v*` tag push — the release tag publishes the site as of that tag, so the
  changelog entry and the pinned `FANOUT_VERSION` go live with the Release (see
  `RELEASE.md`);
- `gh workflow run pages.yml --ref <ref>` — an out-of-band publish for docs
  that should not wait for the next release.

The out-of-band publish takes the whole ref, so `--ref main` also ships release
docs staged for a tag nobody has cut yet: a changelog entry naming a version
whose Release does not exist, and a pinned `FANOUT_VERSION` nobody can install.
Check what `main` is carrying before reaching for it, and pass `--ref <tag>`
when you only want the last release's site back.

The same staging window is why a PR that adds or renames a page under
`content/docs/` and links to it from `README.md` leaves that link 404 on the
repo front page until the next publish. Run the out-of-band publish after
merging one.

Pull request and `main` runs stop after the Hugo + Pagefind build: they skip
`configure-pages`, upload no artifact, and never deploy. The `github-pages`
environment allows deployments from `main` and from `v*` tags; any other ref
fails the deploy job instead of publishing.

To see what is merged but not yet live. The block asks for the deployment
GitHub still marks live rather than the newest record — a rejected or failed
deploy leaves a record too — and every step that could answer wrongly stops it
instead. A stale `origin/main` under-reports; an empty `$last` would make
`git log` compare `HEAD..origin/main` and report nothing pending on an
up-to-date checkout. `set -e` inside the subshell is what makes those exits
reach you.

```bash
(
  set -e
  git fetch origin main --quiet
  last="$(gh api graphql -f query='
    query($owner: String!, $name: String!) {
      repository(owner: $owner, name: $name) {
        deployments(environments: ["github-pages"], last: 100) {
          nodes { commitOid state }
        }
      }
    }' -F owner=butaosuinu -F name=fanout \
    --jq '[.data.repository.deployments.nodes[] | select(.state == "ACTIVE")][0].commitOid')"
  [ -n "$last" ] || { echo "no live github-pages deployment in the last 100 records" >&2; exit 1; }
  git log --oneline "$last"..origin/main -- site/
)
```

The live deployment falls outside that 100-record window only after a failure
streak that long, and the block refuses to answer rather than compare against
nothing.

## Local preview

```bash
hugo server -s site
```

## Search (Pagefind)

Search is powered by [Pagefind](https://pagefind.app/) — a fully static,
build-time index (no external service). The `pages.yml` workflow downloads the
`pagefind_extended` binary (extended = CJK/Japanese segmentation) and runs it
against `site/public` after the Hugo build, emitting `site/public/pagefind/`
(git-ignored; never committed).

A plain `hugo server` does **not** generate that index, so the search modal
opens but returns nothing. To preview search locally, build to disk and let
Pagefind serve it. Override the production `baseURL` (`/fanout/` in `hugo.toml`)
with `/` so assets resolve at Pagefind's server root instead of 404-ing:

```bash
hugo --gc -s site --baseURL / && npx -y pagefind --site site/public --serve
```

The UI is Pagefind's default `PagefindUI`, themed via CSS variables in
`assets/css/main.css`, mounted in a `⌘K` / `/` modal (`layouts/_partials/search.html`,
triggered from `layouts/_partials/nav.html`). Indexing is scoped to docs bodies
via `data-pagefind-body` on `<article class="docs-main">`.
