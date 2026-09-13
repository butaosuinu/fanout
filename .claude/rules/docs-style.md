---
paths:
  - "README*.md"
  - "RELEASE.md"
  - "docs/**"
  - "site/content/docs/**"
---

# Documentation style

User-facing docs follow `docs/doc-style.ja.md` (EN banned-word tables, the
Japanese AI-tell catalog, self-checks). The core of it:

- Match the house style: terse, imperative, active voice; define a term once
  and reuse it; sentence case for subheadings. Read the neighboring doc first
  and mirror its tone.
- Lead with the conclusion. No warm-up openers, no restating the request.
- Cut filler and hedging: `in order to` -> `to`, `utilize` -> `use`,
  「〜することができます」-> 「〜できます」, 「〜となっています」-> 「〜です」.
  Avoid AI-tell vocabulary (delve, seamless, robust, streamline, 「シームレスな」
  「〜していきましょう」); technically precise uses are fine.
- Use the real count, not a reflexive rule of three. One bold phrase per
  section at most. Show specifics: numbers, examples, commands.
- Em dash only as a deliberate aside, never as connective filler.
- Keep pairs in sync: `README.md` with `README.ja.md`, each
  `site/content/docs/*.md` with its `*.ja.md`.
- Before finishing, ask whether each paragraph adds one new fact; cut the ones
  that don't.
