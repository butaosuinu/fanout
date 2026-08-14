package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/gitstat"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const (
	diffDeadline           = 10 * time.Second
	diffMaxFiles           = 500
	diffCollectionMaxBytes = 10 * 1024 * 1024
	diffMaxBytes           = 1 * 1024 * 1024
)

type diffIdentity struct {
	parent string
	issue  int
	task   string
	source string
	byTask bool
}

type diffResponse struct {
	PaneID     string          `json:"paneId"`
	BranchName string          `json:"branchName"`
	BaseBranch string          `json:"baseBranch"`
	MergeBase  string          `json:"mergeBase"`
	CapturedAt string          `json:"capturedAt"`
	Files      []diffFileEntry `json:"files"`
	Patch      string          `json:"patch"`
	Truncated  bool            `json:"truncated"`
	TotalBytes int             `json:"totalBytes"`
}

type diffFileEntry struct {
	Path string `json:"path"`
	// OldPath carries a rename's merge-base path. omitempty on purpose: renames
	// are a minority of entries and every byte competes for diffMaxBytes.
	OldPath       string `json:"oldPath,omitempty"`
	Additions     *int   `json:"additions"`
	Deletions     *int   `json:"deletions"`
	Binary        bool   `json:"binary"`
	PatchIncluded bool   `json:"patchIncluded"`
	OmittedReason string `json:"omittedReason"`
}

type diffPatchGroup struct {
	fileIndex int
	patch     string
}

type diffWorktreeResult struct {
	patch gitstat.Patch
	err   error
}

// noStore wraps a whole endpoint, gate refusals included, for the routes whose
// contract forbids caching any response: /api/diff, and the merge route, where
// a cached 403 or 409 would misreport the state of a mutation.
func noStore(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

// handleDiff serves one merge-base-relative worktree patch selected by the
// stable identity already present in the latest dashboard snapshot. It never
// accepts a filesystem path or requires the recorded runtime pane to be live.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	identity, err := parseDiffIdentity(r.URL.Query())
	if err != nil {
		peekError(w, http.StatusBadRequest, err.Error())
		return
	}

	pv, ok := s.poller.snapshotDiffPane(identity)
	if !ok {
		peekError(w, http.StatusNotFound, "diff identity does not match exactly one current snapshot row")
		return
	}
	if pv.NotStarted || pv.Kind == state.PaneKindShell || pv.WorktreePath == "" {
		peekError(w, http.StatusNotFound, "snapshot row has no recorded worktree")
		return
	}
	info, err := os.Stat(pv.WorktreePath)
	if err != nil || !info.IsDir() {
		peekError(w, http.StatusNotFound, "recorded worktree is no longer available")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	var diffWorktree func(context.Context, string, string) (gitstat.Patch, error)
	if s.diffWorktree != nil {
		injected := s.diffWorktree
		diffWorktree = func(_ context.Context, path, baseRef string) (gitstat.Patch, error) {
			return injected(path, baseRef)
		}
	} else {
		diffWorktree = func(ctx context.Context, path, baseRef string) (gitstat.Patch, error) {
			return (gitstat.Runner{
				Cwd:           s.poller.projectRoot,
				Context:       ctx,
				MaxFiles:      diffMaxFiles,
				MaxPatchBytes: diffCollectionMaxBytes,
			}).WorktreePatch(path, baseRef)
		}
	}
	patch, err := collectWorktreePatch(
		r.Context(),
		diffWorktree,
		pv.WorktreePath,
		pv.BaseBranch,
		diffDeadline,
	)
	if err != nil {
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			peekError(w, http.StatusBadGateway, "diff collection timed out")
			return
		}
		peekError(w, http.StatusBadGateway, "git worktree patch: "+err.Error())
		return
	}

	body, err := marshalDiffResponse(pv, patch, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		peekError(w, http.StatusBadGateway, err.Error())
		return
	}
	// A failed response write means the client went away; nothing to do here.
	_, _ = w.Write(body)
}

func parseDiffIdentity(query url.Values) (diffIdentity, error) {
	parent, parentSet, err := singleDiffQueryValue(query, "parent")
	if err != nil || !parentSet || parent == "" {
		return diffIdentity{}, fmt.Errorf("invalid parent: want exactly one non-empty value")
	}
	issueRaw, issueSet, issueErr := singleDiffQueryValue(query, "issue")
	if issueErr != nil {
		return diffIdentity{}, issueErr
	}
	task, taskSet, taskErr := singleDiffQueryValue(query, "task")
	if taskErr != nil {
		return diffIdentity{}, taskErr
	}
	if issueSet == taskSet {
		return diffIdentity{}, fmt.Errorf("specify exactly one of issue or task")
	}
	source, sourceSet, sourceErr := singleDiffQueryValue(query, "source")
	if sourceErr != nil {
		return diffIdentity{}, sourceErr
	}

	identity := diffIdentity{parent: parent, source: source}
	if taskSet {
		if task == "" {
			return diffIdentity{}, fmt.Errorf("invalid task: want a non-empty value")
		}
		if !sourceSet || source == "" {
			return diffIdentity{}, fmt.Errorf("task rows require a non-empty source")
		}
		identity.task = task
		identity.byTask = true
		return identity, nil
	}

	issue, err := strconv.Atoi(issueRaw)
	if err != nil || issue == 0 || strconv.Itoa(issue) != issueRaw {
		return diffIdentity{}, fmt.Errorf("invalid issue %q: want a non-zero decimal integer", issueRaw)
	}
	if issue > 0 && sourceSet {
		return diffIdentity{}, fmt.Errorf("positive issue rows do not accept source")
	}
	if issue < 0 && (!sourceSet || source == "") {
		return diffIdentity{}, fmt.Errorf("negative issue rows require a non-empty source")
	}
	identity.issue = issue
	return identity, nil
}

func singleDiffQueryValue(query url.Values, key string) (string, bool, error) {
	values, ok := query[key]
	if !ok {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, fmt.Errorf("invalid %s: want exactly one value", key)
	}
	return values[0], true, nil
}

// snapshotDiffPane selects by the public row identity used by the SPA. Exactly
// one match is required so stale or colliding rows never choose a worktree by
// accident. PaneID and Alive are response metadata, not lookup constraints.
func (p *poller) snapshotDiffPane(identity diffIdentity) (sessionview.PaneView, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var match sessionview.PaneView
	matches := 0
	for _, session := range p.latest.Sessions {
		if session.Parent != identity.parent {
			continue
		}
		for i := range session.Panes {
			pv := session.Panes[i]
			matched := false
			if identity.byTask {
				matched = pv.TaskID == identity.task && pv.SourceKey == identity.source
			} else {
				matched = pv.TaskID == "" && pv.IssueNum == identity.issue && pv.SourceKey == identity.source
			}
			if !matched {
				continue
			}
			match = pv
			matches++
		}
	}
	return match, matches == 1
}

func collectWorktreePatch(
	ctx context.Context,
	diffWorktree func(context.Context, string, string) (gitstat.Patch, error),
	path string,
	baseRef string,
	timeout time.Duration,
) (gitstat.Patch, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := make(chan diffWorktreeResult, 1)
	go func() {
		patch, err := diffWorktree(ctx, path, baseRef)
		result <- diffWorktreeResult{patch: patch, err: err}
	}()

	select {
	case got := <-result:
		return got.patch, got.err
	case <-ctx.Done():
		return gitstat.Patch{}, ctx.Err()
	}
}

func marshalDiffResponse(
	pv sessionview.PaneView,
	patch gitstat.Patch,
	capturedAt string,
) ([]byte, error) {
	if len(patch.Files) > diffMaxFiles {
		return nil, fmt.Errorf("diff contains %d files; limit is %d", len(patch.Files), diffMaxFiles)
	}

	response := diffResponse{
		PaneID:     pv.PaneID,
		BranchName: pv.BranchName,
		BaseBranch: pv.BaseBranch,
		MergeBase:  patch.MergeBase,
		CapturedAt: capturedAt,
		Files:      make([]diffFileEntry, len(patch.Files)),
	}
	var includedIndexes []int
	for i, stat := range patch.Files {
		if !validDiffFileStat(stat) {
			return nil, fmt.Errorf("diff file metadata for %q is inconsistent", stat.Path)
		}
		response.Files[i] = diffFileEntry{
			Path:          stat.Path,
			OldPath:       stat.OldPath,
			Additions:     new(stat.Additions),
			Deletions:     new(stat.Deletions),
			Binary:        stat.Binary,
			PatchIncluded: stat.PatchIncluded,
			OmittedReason: stat.OmittedReason,
		}
		if stat.OmittedReason == "collectionLimit" {
			response.Files[i].Additions = nil
			response.Files[i].Deletions = nil
			response.Truncated = true
		}
		if stat.PatchIncluded {
			includedIndexes = append(includedIndexes, i)
		}
	}

	rawGroups, err := splitDiffPatchGroups(patch.Patch)
	if err != nil {
		return nil, err
	}
	if len(rawGroups) != len(includedIndexes) {
		return nil, fmt.Errorf(
			"diff patch has %d file groups but metadata marks %d files included",
			len(rawGroups),
			len(includedIndexes),
		)
	}

	collected := make([]diffPatchGroup, 0, len(rawGroups))
	collectionFull := false
	for i, group := range rawGroups {
		fileIndex := includedIndexes[i]
		if collectionFull || len(group) > diffCollectionMaxBytes-response.TotalBytes {
			collectionFull = true
			response.Truncated = true
			file := &response.Files[fileIndex]
			file.Additions = nil
			file.Deletions = nil
			file.PatchIncluded = false
			file.OmittedReason = "collectionLimit"
			continue
		}
		response.TotalBytes += len(group)
		collected = append(collected, diffPatchGroup{fileIndex: fileIndex, patch: group})
	}
	collectionTruncated := response.Truncated

	var patchBuilder strings.Builder
	patchBuilder.Grow(response.TotalBytes)
	for _, group := range collected {
		patchBuilder.WriteString(group.patch)
	}
	response.Patch = patchBuilder.String()
	body, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode diff response: %w", err)
	}
	if len(body) <= diffMaxBytes {
		return body, nil
	}

	response.Patch = ""
	response.Truncated = collectionTruncated || len(collected) > 0
	for _, group := range collected {
		file := &response.Files[group.fileIndex]
		file.PatchIncluded = false
		file.OmittedReason = "responseLimit"
	}
	body, err = json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode diff response metadata: %w", err)
	}
	if len(body) > diffMaxBytes {
		return nil, fmt.Errorf("diff response metadata exceeds %d bytes", diffMaxBytes)
	}

	acceptedPatch := ""
	acceptedBody := body
	for i, group := range collected {
		file := &response.Files[group.fileIndex]
		file.PatchIncluded = true
		file.OmittedReason = ""
		candidatePatch := acceptedPatch + group.patch
		response.Patch = candidatePatch
		response.Truncated = collectionTruncated || i+1 < len(collected)
		candidateBody, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode bounded diff response: %w", marshalErr)
		}
		if len(candidateBody) > diffMaxBytes {
			file.PatchIncluded = false
			file.OmittedReason = "responseLimit"
			response.Patch = acceptedPatch
			break
		}
		acceptedPatch = candidatePatch
		acceptedBody = candidateBody
	}
	return acceptedBody, nil
}

func validDiffFileStat(stat gitstat.FileStat) bool {
	// A rename names two distinct paths. Re-asserted here because the SPA keys
	// the sidebar by Path and would anchor a self-rename to nothing.
	if stat.OldPath != "" && stat.OldPath == stat.Path {
		return false
	}
	switch stat.OmittedReason {
	case "":
		return stat.PatchIncluded && !stat.Binary
	case "binary":
		return !stat.PatchIncluded && stat.Binary
	case "tooLarge", "collectionLimit":
		return !stat.PatchIncluded && !stat.Binary
	default:
		return false
	}
}

// splitDiffPatchGroups finds complete diff --git blocks and coalesces adjacent
// blocks with the same header. The latter preserves file-type replacements as
// one indivisible file group without recovering paths from quoted headers.
func splitDiffPatchGroups(patch string) ([]string, error) {
	if patch == "" {
		return []string{}, nil
	}
	const marker = "diff --git "
	if !strings.HasPrefix(patch, marker) {
		return nil, fmt.Errorf("diff patch does not start with %q", marker)
	}

	starts := []int{0}
	for searchFrom := len(marker); searchFrom < len(patch); {
		next := strings.Index(patch[searchFrom:], "\n"+marker)
		if next < 0 {
			break
		}
		start := searchFrom + next + 1
		starts = append(starts, start)
		searchFrom = start + len(marker)
	}

	groups := make([]string, 0, len(starts))
	lastHeader := ""
	for i, start := range starts {
		end := len(patch)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		block := patch[start:end]
		header, _, found := strings.Cut(block, "\n")
		if !found {
			return nil, fmt.Errorf("diff patch block has no header terminator")
		}
		if len(groups) > 0 && header == lastHeader {
			groups[len(groups)-1] += block
			continue
		}
		groups = append(groups, block)
		lastHeader = header
	}
	return groups, nil
}
