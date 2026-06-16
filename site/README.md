# fanout docs site

Hugo (Extended) site published to <https://butaosuinu.github.io/fanout/>.
Custom theme **PAPER BREEZE** (no external theme/module); bilingual via
`*.md` (en) / `*.ja.md` (ja). Deployed by `.github/workflows/pages.yml`.

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
