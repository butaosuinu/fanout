package ghissue

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestPRStacks(t *testing.T) {
	mergedAt := "2026-09-01T00:00:00Z"
	stacked := &PRStack{
		Number: 12, Size: 3, BaseRef: "main", Position: 2,
		Entries: []PRStackEntry{
			{Position: 1, PR: PRRef{Number: 843, State: "MERGED", MergedAt: &mergedAt, HeadRef: "stack/a"}},
			{Position: 2, PR: PRRef{Number: 844, State: "OPEN", ReviewDecision: "APPROVED", HeadRef: "stack/b"}},
		},
	}
	tests := []struct {
		name   string
		output string
		// exit is gh's status: real gh exits 1 whenever the response carries
		// any GraphQL error, and still prints the response.
		exit    int
		want    map[int]*PRStack
		wantErr string
	}{
		{
			name: "keeps a stack whose error points at one unreadable layer",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":{"position":2},"stack":{"number":12,"size":3,"baseRefName":"main","entries":{"nodes":[
			  {"position":1,"pullRequest":{"number":843,"state":"MERGED","mergedAt":"2026-09-01T00:00:00Z","isDraft":false,"reviewDecision":"","headRefName":"stack/a"}},
			  {"position":2,"pullRequest":{"number":844,"state":"OPEN","mergedAt":null,"isDraft":false,"reviewDecision":"APPROVED","headRefName":"stack/b"}},
			  {"position":3,"pullRequest":null}
			]}}},"pr_845":{"stackEntry":null,"stack":null}}},
			  "errors":[{"message":"Could not resolve to a node","path":["repository","pr_844","stack","entries","nodes",2,"pullRequest"]}]}`,
			exit:    1,
			want:    map[int]*PRStack{844: stacked, 845: nil},
			wantErr: "pr_844: graphql: Could not resolve to a node",
		},
		{
			name: "drops only the pull request whose alias failed",
			output: `{"data":{"repository":{"pr_844":null,"pr_845":{"stackEntry":null,"stack":null}}},
			  "errors":[{"message":"Could not resolve to a PullRequest with the number of 844.","path":["repository","pr_844"]}]}`,
			exit:    1,
			want:    map[int]*PRStack{845: nil},
			wantErr: "pr_844: graphql: Could not resolve",
		},
		{
			name:    "drops the chunk on a query-wide error",
			output:  `{"errors":[{"message":"Field 'stack' doesn't exist on type 'PullRequest'","path":["query"]}]}`,
			exit:    1,
			want:    map[int]*PRStack{},
			wantErr: "Field 'stack' doesn't exist",
		},
		{
			name: "treats a membership nulled by an error as unread, not as no stack",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":{"position":2},"stack":null},"pr_845":{"stackEntry":null,"stack":null}}},
			  "errors":[{"message":"Something went wrong while executing your query.","path":["repository","pr_844","stack"]}]}`,
			exit:    1,
			want:    map[int]*PRStack{845: nil},
			wantErr: "pr_844: graphql: Something went wrong",
		},
		{
			name:   "treats a null pull request without an error as unread",
			output: `{"data":{"repository":{"pr_844":null,"pr_845":{"stackEntry":null,"stack":null}}}}`,
			want:   map[int]*PRStack{845: nil},
		},
		{
			name:    "reads nothing when gh fails without a response",
			exit:    1,
			want:    map[int]*PRStack{},
			wantErr: "gh api graphql pr stacks",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Unlike installFakeGHWithResult, print the response before exiting,
			// the way gh does on a GraphQL error.
			argsPath := installFakeGHScript(t, `
printf '%s\n' "$@" > "$GH_FAKE_ARGS"
printf '%s' "$GH_FAKE_OUTPUT"
exit "$GH_FAKE_EXIT"
`)
			t.Setenv("GH_FAKE_OUTPUT", tt.output)
			t.Setenv("GH_FAKE_EXIT", strconv.Itoa(tt.exit))

			got, err := (Runner{}).PRStacks("owner", "repo", []int{844, 845})
			if tt.wantErr == "" && err != nil {
				t.Fatalf("PRStacks() error = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("PRStacks() error = %v, want %q", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PRStacks() = %#v, want %#v", got, tt.want)
			}

			data, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			args := string(data)
			// -f, never -F: an owner or repo that parses as JSON would otherwise
			// be sent as a non-string and fail String!.
			if strings.Contains(args, "\n-F\n") {
				t.Fatalf("PRStacks() used -F for a String! variable:\n%s", args)
			}
			for _, want := range []string{"-f\nowner=owner", "-f\nrepo=repo", "pr_844: pullRequest(number: 844)", "pr_845: pullRequest(number: 845)"} {
				if !strings.Contains(args, want) {
					t.Fatalf("PRStacks() args missing %q:\n%s", want, args)
				}
			}
		})
	}
}

// TestPRStack pins the merge fence's live read: unlike PRStacks it keeps no
// partial answer, so an error anywhere in the response is the fence's refusal.
func TestPRStack(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		exit    int
		want    *PRStack
		wantErr bool
	}{
		{
			name: "reads the layers below",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":{"position":2},"stack":{"number":12,"size":2,"baseRefName":"main","entries":{"nodes":[
			  {"position":1,"pullRequest":{"number":843,"state":"OPEN","mergedAt":null,"isDraft":false,"reviewDecision":"","headRefName":"stack/a"}},
			  {"position":2,"pullRequest":{"number":844,"state":"OPEN","mergedAt":null,"isDraft":false,"reviewDecision":"","headRefName":"stack/b"}}
			]}}}}}}`,
			want: &PRStack{Number: 12, Size: 2, BaseRef: "main", Position: 2, Entries: []PRStackEntry{
				{Position: 1, PR: PRRef{Number: 843, State: "OPEN", HeadRef: "stack/a"}},
				{Position: 2, PR: PRRef{Number: 844, State: "OPEN", HeadRef: "stack/b"}},
			}},
		},
		{
			name:   "a pull request in no stack is nil",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":null,"stack":null}}}}`,
		},
		{
			// PRStacks would keep the readable layers; a fence must not.
			name: "an error inside the stack refuses",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":{"position":2},"stack":{"number":12,"size":2,"baseRefName":"main","entries":{"nodes":[
			  {"position":1,"pullRequest":null}]}}}}},
			  "errors":[{"message":"Could not resolve to a node","path":["repository","pr_844","stack","entries","nodes",0,"pullRequest"]}]}`,
			exit:    1,
			wantErr: true,
		},
		{
			// A server whose schema has no stack has no native stacks to fence.
			// captured from real gh 2.92.0 against an undefined field
			name:   "a schema without the stack fields reads as no stack",
			output: `{"errors":[{"path":["query","repository","pr_844","stackEntry"],"extensions":{"code":"undefinedField","typeName":"PullRequest","fieldName":"stackEntry"},"message":"Field 'stackEntry' doesn't exist on type 'PullRequest'"}]}`,
			exit:   1,
		},
		{
			name:    "a null pull request is an error, not a pull request in no stack",
			output:  `{"data":{"repository":{"pr_844":null}}}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGHScript(t, `
printf '%s\n' "$@" > "$GH_FAKE_ARGS"
printf '%s' "$GH_FAKE_OUTPUT"
exit "$GH_FAKE_EXIT"
`)
			t.Setenv("GH_FAKE_OUTPUT", tt.output)
			t.Setenv("GH_FAKE_EXIT", strconv.Itoa(tt.exit))

			got, err := (Runner{}).PRStack(t.Context(), "owner", "repo", 844)
			if (err != nil) != tt.wantErr {
				t.Fatalf("PRStack() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PRStack() = %#v, want %#v", got, tt.want)
			}
			data, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			// One page of 100 entries: a layer the read does not return counts as
			// unmerged, so the 20 the maps draw would refuse layer 22 forever.
			args := string(data)
			for _, want := range []string{"pr_844: pullRequest(number: 844)", "entries(first: 100)"} {
				if strings.Contains(args, "\n-F\n") || !strings.Contains(args, want) {
					t.Fatalf("PRStack() args = %q, want -f variables and %q", args, want)
				}
			}
		})
	}
}
