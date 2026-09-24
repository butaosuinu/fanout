package ghissue

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
)

// PRStack is a pull request's place in a GitHub-native stacked pull request
// (public preview since 2026-07): layer Position of Size, counted from the
// layer closest to BaseRef.
//
// It is display only. Merge, branch delete, the merge-hold release, and every
// derived row field read the row's own PRRefs and never look inside Entries:
// the entry copies are slim and fetched at a different time from the row's.
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

// prStackFields reads one pull request's stack. It stays out of
// prRefNodeFields on purpose: the stack schema is a preview, and a rename there
// would fail every PR read the TUI and the CLI merge gates share. Here a failure
// only hides the stack maps.
//
// ponytail: 20 layers per stack; a taller one shows its true size in the
// heading but draws only the first 20. Page entries if stacks get that tall.
const prStackFields = ` {
      stackEntry { position }
      stack {
        number
        size
        baseRefName
        entries(first: 20) {
          nodes {
            position
            pullRequest { number state mergedAt isDraft reviewDecision headRefName }
          }
        }
      }
    }`

func prStacksQuery(nums []int) string {
	fields := make([]string, 0, len(nums))
	for _, num := range nums {
		n := strconv.Itoa(num)
		fields = append(fields, "    pr_"+n+": pullRequest(number: "+n+")"+prStackFields)
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
			"-f", "query="+prStacksQuery(chunk),
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
			continue // defensive: the schema types pullRequest as nullable
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
	failed := map[string]bool{}
	var loadErr error
	for _, graphErr := range root.Errors {
		alias := aliasFromPath(graphErr.Path, "pr_")
		if alias == "" {
			return nil, fmt.Errorf("gh api graphql pr stacks: %s", graphErr.Message)
		}
		failed[alias] = true
		loadErr = errors.Join(loadErr, fmt.Errorf("%s: graphql: %s", alias, graphErr.Message))
	}
	stacks := make(map[int]*PRStack, len(nums))
	for _, num := range nums {
		alias := "pr_" + strconv.Itoa(num)
		// A null alias without an error is a failed read, not "no stack", the
		// same as in parseIssueDetailsBatch: dropping it keeps the last known
		// stack instead of erasing it.
		node := root.Data.Repository[alias]
		if node == nil || failed[alias] {
			continue
		}
		stacks[num] = node.stack()
	}
	return stacks, loadErr
}
