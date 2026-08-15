package tmuxbackend

// ResetLayoutMemoForTest clears the package-level relayout dedup memo so a
// test that drives the real Relayout does not leak its window signature into
// later tests in the same binary.
func ResetLayoutMemoForTest() {
	defaultLayoutApplier.mu.Lock()
	defer defaultLayoutApplier.mu.Unlock()
	clear(defaultLayoutApplier.memo)
}
