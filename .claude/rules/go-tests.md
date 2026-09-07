---
paths:
  - "**/*_test.go"
---

# Go test conventions

Table-driven tests read case by case from `go test -v`, not only from the
function name. `internal/infra/team/detect_test.go` is the model.

- Give every case a `name` and run it as
  `t.Run(tt.name, func(t *testing.T) { ... })`, so `go test -run TestX/case_name`
  isolates one case and a failure names the case that broke.
- `name` states the behavior or edge being pinned, not the input echoed back:
  `"trims surrounding whitespace"`, not `"  running  "`.
- Use field-named struct literals once a case has more than three or four
  fields; positional literals past that are unreadable and break on every
  field addition.
- Keep cases in a slice, not a `map`: map order is undefined and keys cannot
  become subtest names.
- Failure messages use the `funcName(input) = got, want` form the suite
  already uses.
- Comment a case only when its purpose is not obvious from the values
  (boundary, precedence, why this input). Keep provenance comments on opaque
  golden values (`// captured from real tmux 3.6a`) and the one-line comment
  above a test that states what it guarantees.
- Leave existing loop-variable naming alone (`cases`/`tc` and `tests`/`tt`
  both occur); new tables use `tests`/`tt`.
