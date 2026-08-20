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
- `gh workflow run pages.yml --ref main` — an out-of-band publish for docs that
  should not wait for the next release.

Pull request and `main` runs stop after the Hugo + Pagefind build: they skip
`configure-pages`, upload no artifact, and never deploy. The `github-pages`
environment allows deployments from `main` and from `v*` tags; any other ref
fails the deploy job instead of publishing.

To see what is merged but not yet live:

```bash
last="$(gh api "repos/butaosuinu/fanout/deployments?environment=github-pages&per_page=1" \
  --jq '.[0].sha')"
git log --oneline "$last"..origin/main -- site/
```

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
