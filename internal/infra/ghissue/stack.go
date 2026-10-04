package ghissue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/errs"
)

// PRStack is a pull request's place in a GitHub-native stacked pull request
// (public preview since 2026-07): layer Position of Size, counted from the
// layer closest to BaseRef.
//
// The snapshot's copy is display only. Merge, branch delete, the merge-hold
// release, and every derived row field read the row's own PRRefs and never
// look inside its Entries: the entry copies are slim and fetched at a different
// time from the row's. The merge fence reads its own live copy (Runner.PRStack).
type PRStack struct {
	Number   int            `json:"number"`
	Size     int            `json:"size"`
	BaseRef  string         `json:"baseRef"`
	Position int            `json:"position"`
	Entries  []PRStackEntry `json:"entries,omitempty"`
}

// PRStackEntry is one layer of a stack. PR carries only number, state, draft,
// review decision, and head branch.
type PRStackEntry struct {
	Position int   `json:"position"`
	PR       PRRef `json:"pr"`
}

// prStackFields reads one pull request's stack, entries layers of it. It stays
// out of prRefNodeFields on purpose: the stack schema is a preview, and a rename
// there would fail every PR read the TUI and the CLI merge gates share. Here a
// failure only hides the stack maps, or refuses the merge PRStack fences.
func prStackFields(entries int) string {
	return ` {
      stackEntry { position }
      stack {
        number
        size
        baseRefName
        entries(first: ` + strconv.Itoa(entries) + `) {
          nodes {
            position
            pullRequest { number state mergedAt isDraft reviewDecision headRefName }
          }
        }
      }
    }`
}

// The maps draw 20 layers; a taller stack shows its true size in the heading
// but draws only the first 20. The merge fence reads up to GitHub's page size,
// so every layer below position 101 is seen.
//
// ponytail: one page of entries each; page them if stacks get that tall.
const (
	stackMapEntries   = 20
	stackFenceEntries = 100
)

func prStacksQuery(nums []int, entries int) string {
	fields := make([]string, 0, len(nums))
	for _, num := range nums {
		n := strconv.Itoa(num)
		fields = append(fields, "    pr_"+n+": pullRequest(number: "+n+")"+prStackFields(entries))
	}
	return `query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
` + strings.Join(fields, "\n") + `
  }
}`
}

// PRStacks reads the stack of each numbered pull request in owner/repo, in
// aliased batches like IssuesSnapshotWithPRs. The map holds every pull request
// that was read: nil for one outside any stack. A pull request missing from
// the map was not read, and the error says why.
func (r Runner) PRStacks(owner, repo string, nums []int) (map[int]*PRStack, error) {
	stacks := make(map[int]*PRStack, len(nums))
	var loadErr error
	for start := 0; start < len(nums); start += issueDetailsBatchSize {
		chunk := nums[start:min(start+issueDetailsBatchSize, len(nums))]
		out, err := r.gh(
			"api", "graphql",
			"-f", "owner="+owner,
			"-f", "repo="+repo,
			"-f", "query="+prStacksQuery(chunk, stackMapEntries),
		)
		// gh exits non-zero whenever the response carries any GraphQL error, yet
		// still prints it. Read what came back, so one unresolvable pull request
		// drops only itself instead of its whole chunk.
		if err != nil {
			loadErr = errors.Join(loadErr, fmt.Errorf("gh api graphql pr stacks: %w", err))
			if !json.Valid(out) {
				continue
			}
		}
		parsed, err := parsePRStacks(out, chunk)
		loadErr = errors.Join(loadErr, err)
		maps.Copy(stacks, parsed)
	}
	return stacks, loadErr
}

// PRStack reads one pull request's stack as GitHub reports it now, bound to
// ctx so a handler deadline kills gh. nil means the pull request is in no
// stack. Unlike PRStacks it keeps no partial answer: the merge fence refuses on
// any error rather than guess which layers it could not see.
//
// The one error read as "no stack" is a schema without the fields. A server
// whose schema has no stack (a GitHub Enterprise Server without the preview,
// say) has no native stacks, and refusing there would stop every merge. A renamed preview reads the same
// way; a mid-stack layer then still heads its base from the layer below, which
// the merge's chain fence refuses on stable fields.
func (r Runner) PRStack(ctx context.Context, owner, repo string, number int) (_ *PRStack, err error) {
	defer errs.Wrap(&err, "read stack of pull request #%d", number)

	out, err := r.ghContext(ctx,
		"api", "graphql",
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-f", "query="+prStacksQuery([]int{number}, stackFenceEntries),
	)
	if err != nil {
		if noStackSchema(out) {
			return nil, nil
		}
		return nil, err
	}
	stacks, err := parsePRStacks(out, []int{number})
	if err != nil {
		return nil, err
	}
	stack, ok := stacks[number]
	if !ok {
		return nil, errors.New("pull request not found in response")
	}
	return stack, nil
}

// noStackSchema reports a response whose every error is an undefined field:
// the schema, not this pull request, lacks what the query asked for.
func noStackSchema(out []byte) bool {
	var root struct {
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(out, &root) != nil || len(root.Errors) == 0 {
		return false
	}
	for _, e := range root.Errors {
		if e.Extensions.Code != "undefinedField" {
			return false
		}
	}
	return true
}

type prStackNode struct {
	StackEntry *struct {
		Position int `json:"position"`
	} `json:"stackEntry"`
	Stack *struct {
		Number      int    `json:"number"`
		Size        int    `json:"size"`
		BaseRefName string `json:"baseRefName"`
		Entries     struct {
			Nodes []struct {
				Position    int                  `json:"position"`
				PullRequest *prStackEntryGraphQL `json:"pullRequest"`
			} `json:"nodes"`
		} `json:"entries"`
	} `json:"stack"`
}

type prStackEntryGraphQL struct {
	Number         int     `json:"number"`
	State          string  `json:"state"`
	MergedAt       *string `json:"mergedAt"`
	IsDraft        bool    `json:"isDraft"`
	ReviewDecision string  `json:"reviewDecision"`
	HeadRefName    string  `json:"headRefName"`
}

func (pr prStackEntryGraphQL) ref() PRRef {
	return PRRef{
		Number:         pr.Number,
		State:          pr.State,
		MergedAt:       pr.MergedAt,
		IsDraft:        pr.IsDraft,
		ReviewDecision: pr.ReviewDecision,
		HeadRef:        pr.HeadRefName,
	}
}

// unread reports a failed read of one pull request, which PRStacks leaves out so
// the caller keeps its last known stack instead of erasing it: a null alias (as
// parseIssueDetailsBatch treats a null issue), or a null membership field next
// to an error for this pull request (erred). An error deeper inside a returned
// stack, such as one unreadable layer, leaves the rest readable, and stack()
// skips the null.
func (n *prStackNode) unread(erred bool) bool {
	return n == nil || (erred && (n.Stack == nil || n.StackEntry == nil))
}

func (n *prStackNode) stack() *PRStack {
	if n.Stack == nil || n.StackEntry == nil {
		return nil
	}
	s := &PRStack{
		Number:   n.Stack.Number,
		Size:     n.Stack.Size,
		BaseRef:  n.Stack.BaseRefName,
		Position: n.StackEntry.Position,
	}
	for _, e := range n.Stack.Entries.Nodes {
		if e.PullRequest == nil {
			continue // a layer GitHub would not return; its error is in errors
		}
		s.Entries = append(s.Entries, PRStackEntry{Position: e.Position, PR: e.PullRequest.ref()})
	}
	return s
}

func parsePRStacks(out []byte, nums []int) (map[int]*PRStack, error) {
	var root struct {
		Data struct {
			Repository map[string]*prStackNode `json:"repository"`
		} `json:"data"`
		Errors []issueDetailsGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("parse gh api graphql pr stacks: %w", err)
	}
	var loadErr error
	erred := map[string]bool{}
	for _, graphErr := range root.Errors {
		alias := aliasFromPath(graphErr.Path, "pr_")
		if alias == "" {
			return nil, fmt.Errorf("gh api graphql pr stacks: %s", graphErr.Message)
		}
		erred[alias] = true
		loadErr = errors.Join(loadErr, fmt.Errorf("%s: graphql: %s", alias, graphErr.Message))
	}
	stacks := make(map[int]*PRStack, len(nums))
	for _, num := range nums {
		alias := "pr_" + strconv.Itoa(num)
		if node := root.Data.Repository[alias]; !node.unread(erred[alias]) {
			stacks[num] = node.stack()
		}
	}
	return stacks, loadErr
}
