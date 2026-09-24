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
			name: "fills a stacked pull request and skips layers the viewer cannot see",
			output: `{"data":{"repository":{"pr_844":{"stackEntry":{"position":2},"stack":{"number":12,"size":3,"baseRefName":"main","entries":{"nodes":[
			  {"position":1,"pullRequest":{"number":843,"state":"MERGED","mergedAt":"2026-09-01T00:00:00Z","isDraft":false,"reviewDecision":"","headRefName":"stack/a"}},
			  {"position":2,"pullRequest":{"number":844,"state":"OPEN","mergedAt":null,"isDraft":false,"reviewDecision":"APPROVED","headRefName":"stack/b"}},
			  {"position":3,"pullRequest":null}
			]}}},"pr_845":{"stackEntry":null,"stack":null}}}}`,
			want: map[int]*PRStack{844: stacked, 845: nil},
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
