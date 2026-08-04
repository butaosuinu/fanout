// Package errs holds the shared error-wrapping helper. It lives under core
// because core may import only core: a helper every layer needs has nowhere
// else to live.
package errs

import "fmt"

// Wrap prefixes *errp with a formatted message, keeping the chain intact for
// errors.Is/As. It is a no-op when *errp is nil, so it is meant to be deferred
// at the top of a function with a named error return:
//
//	func (r Runner) WorktreePatch(path, baseRef string) (_ Patch, err error) {
//		defer errs.Wrap(&err, "worktree patch %q", path)
//
// The deferred call runs after the return values are assigned, so an err
// shadowed by := inside an if block is still wrapped correctly.
//
// Register it as the function's first defer. Defers run LIFO, so the first
// registered runs last and also wraps what a later cleanup defer assigned to
// err (a Close failure, say).
func Wrap(errp *error, format string, args ...any) {
	if *errp == nil {
		return
	}
	*errp = fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), *errp)
}
