// Package fanset holds pure generic set and selection helpers shared by the
// issue fan-out lane (int issue numbers) and the plan fan-out lane (string
// task ids). Keeping the selection algebra in one place lets both lanes share
// the exact same nil/empty-slice and append-order semantics their dry-run
// golden output depends on.
package fanset

// Set builds a membership set keyed by K.
func Set[K comparable](keys []K) map[K]bool {
	set := make(map[K]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// Union merges membership sets into a fresh, always-non-nil map: a key present
// in any input is present in the result. It replaces the per-lane mergeFanned
// helpers, whose only job was to fold the same-parent and worktree-fallback
// fanned sets together.
func Union[K comparable](sets ...map[K]bool) map[K]bool {
	out := map[K]bool{}
	for _, s := range sets {
		for k := range s {
			out[k] = true
		}
	}
	return out
}

// FilterOnlySkip partitions items by the --only / --skip selectors keyed by K.
// It returns the kept items, the items dropped for being outside --only, the
// items dropped for being in --skip, and the --only keys absent from items.
//
// The branch order, append order, and the early nil-return when neither
// selector is set are load-bearing: the dry-run "filtered out:" section is
// emitted only when FilteredOnly/FilteredSkip are non-empty, and its row order
// mirrors the input order.
func FilterOnlySkip[T any, K comparable](items []T, key func(T) K, only, skip []K) (kept, filteredOnly, filteredSkip []T, missingOnly []K) {
	if len(only) == 0 && len(skip) == 0 {
		return items, nil, nil, nil
	}

	present := map[K]bool{}
	for _, item := range items {
		present[key(item)] = true
	}
	for _, k := range only {
		if !present[k] {
			missingOnly = append(missingOnly, k)
		}
	}

	onlySet := Set(only)
	skipSet := Set(skip)
	for _, item := range items {
		k := key(item)
		switch {
		case len(only) > 0 && !onlySet[k]:
			filteredOnly = append(filteredOnly, item)
		case len(skip) > 0 && skipSet[k]:
			filteredSkip = append(filteredSkip, item)
		default:
			kept = append(kept, item)
		}
	}
	return kept, filteredOnly, filteredSkip, missingOnly
}

// SplitFanned partitions items into those not yet fanned out (targets) and the
// keys of those already fanned (skipped), preserving input order.
func SplitFanned[T any, K comparable](items []T, key func(T) K, fanned map[K]bool) (targets []T, skipped []K) {
	for _, item := range items {
		k := key(item)
		if fanned[k] {
			skipped = append(skipped, k)
			continue
		}
		targets = append(targets, item)
	}
	return targets, skipped
}

// ApplyLimit caps items to the first limit entries and returns the remainder as
// deferred. A non-positive limit or a slice already within the limit defers
// nothing (nil).
func ApplyLimit[T any](items []T, limit int) (targets, deferred []T) {
	if limit > 0 && len(items) > limit {
		return items[:limit], items[limit:]
	}
	return items, nil
}
