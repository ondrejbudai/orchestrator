package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeMultiArgCapture creates a shell script that writes its arguments to a
// file and prints the corresponding canned stdout based on invocation count.
func writeMultiArgCapture(t *testing.T, stdouts []string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "capture")
	outFile := filepath.Join(dir, "args.txt")
	countFile := filepath.Join(dir, "count")

	// Script increments a counter file and prints the n-th stdout.
	var cases string
	for i, s := range stdouts {
		cases += "  " + itoa(i) + ") printf '%s' '" + s + "' ;;\n"
	}

	content := `#!/bin/sh
echo '---' >> ` + outFile + `
printf '%s\n' "$@" >> ` + outFile + `
if [ -f ` + countFile + ` ]; then
  n=$(cat ` + countFile + `)
else
  n=0
fi
echo $((n + 1)) > ` + countFile + `
case $n in
` + cases + `esac
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script, outFile
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func TestListIssueComments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	tests := []struct {
		name      string
		stdout    string
		repo      string
		prNumber  int
		wantCount int
		wantArgs  []string
	}{
		{
			name:      "empty comments",
			stdout:    "[]",
			repo:      "org/repo",
			prNumber:  1,
			wantCount: 0,
			wantArgs:  []string{"api", "--paginate", "/repos/org/repo/issues/1/comments"},
		},
		{
			name:      "single comment",
			stdout:    `[{"id":100,"body":"looks good","user":{"login":"alice"},"created_at":"2025-01-01T00:00:00Z"}]`,
			repo:      "org/repo",
			prNumber:  42,
			wantCount: 1,
			wantArgs:  []string{"api", "--paginate", "/repos/org/repo/issues/42/comments"},
		},
		{
			name:      "multiple comments",
			stdout:    `[{"id":100,"body":"first","user":{"login":"alice"},"created_at":"2025-01-01T00:00:00Z"},{"id":101,"body":"second","user":{"login":"bob"},"created_at":"2025-01-01T01:00:00Z"}]`,
			repo:      "org/repo",
			prNumber:  5,
			wantCount: 2,
			wantArgs:  []string{"api", "--paginate", "/repos/org/repo/issues/5/comments"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, outFile := writeArgCapture(t, tt.stdout)
			r := New(script)

			comments, err := r.ListIssueComments(context.Background(), tt.repo, tt.prNumber)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(comments) != tt.wantCount {
				t.Errorf("got %d comments, want %d", len(comments), tt.wantCount)
			}
			for _, c := range comments {
				if c.Type != IssueComment {
					t.Errorf("comment %d has type %q, want %q", c.ID, c.Type, IssueComment)
				}
			}

			gotArgs := readArgs(t, outFile)
			if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestListReviewComments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	tests := []struct {
		name      string
		stdout    string
		repo      string
		prNumber  int
		wantCount int
		wantArgs  []string
	}{
		{
			name:      "empty review comments",
			stdout:    "[]",
			repo:      "org/repo",
			prNumber:  1,
			wantCount: 0,
			wantArgs:  []string{"api", "--paginate", "/repos/org/repo/pulls/1/comments"},
		},
		{
			name:      "single review comment",
			stdout:    `[{"id":200,"body":"nit: rename this","user":{"login":"reviewer"},"created_at":"2025-01-01T00:00:00Z","path":"main.go","diff_hunk":"@@ -1,3 +1,4 @@"}]`,
			repo:      "org/repo",
			prNumber:  10,
			wantCount: 1,
			wantArgs:  []string{"api", "--paginate", "/repos/org/repo/pulls/10/comments"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, outFile := writeArgCapture(t, tt.stdout)
			r := New(script)

			comments, err := r.ListReviewComments(context.Background(), tt.repo, tt.prNumber)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(comments) != tt.wantCount {
				t.Errorf("got %d comments, want %d", len(comments), tt.wantCount)
			}
			for _, c := range comments {
				if c.Type != ReviewComment {
					t.Errorf("comment %d has type %q, want %q", c.ID, c.Type, ReviewComment)
				}
			}

			gotArgs := readArgs(t, outFile)
			if !equalArgs(gotArgs, tt.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestIsPROpen(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	tests := []struct {
		name     string
		stdout   string
		wantOpen bool
	}{
		{name: "open PR", stdout: "open\n", wantOpen: true},
		{name: "closed PR", stdout: "closed\n", wantOpen: false},
		{name: "merged PR", stdout: "closed\n", wantOpen: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script, _ := writeArgCapture(t, tt.stdout)
			r := New(script)

			open, err := r.IsPROpen(context.Background(), "org/repo", 1)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if open != tt.wantOpen {
				t.Errorf("IsPROpen() = %v, want %v", open, tt.wantOpen)
			}
		})
	}
}

func TestFetchAllComments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	// The script is called twice: first for issue comments, then for review comments.
	issueJSON := `[{"id":100,"body":"issue comment","user":{"login":"alice"},"created_at":"2025-01-01T00:00:00Z"}]`
	reviewJSON := `[{"id":50,"body":"review comment","user":{"login":"bob"},"created_at":"2025-01-01T01:00:00Z"}]`

	script, _ := writeMultiArgCapture(t, []string{issueJSON, reviewJSON})
	r := New(script)

	comments, err := r.FetchAllComments(context.Background(), "org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	// Should be sorted by ID: 50 first, then 100
	if comments[0].ID != 50 {
		t.Errorf("first comment ID = %d, want 50", comments[0].ID)
	}
	if comments[1].ID != 100 {
		t.Errorf("second comment ID = %d, want 100", comments[1].ID)
	}
}

func TestParseComments_Pagination(t *testing.T) {
	// gh api --paginate concatenates JSON arrays
	input := `[{"id":1,"body":"a","user":{"login":"x"},"created_at":"2025-01-01T00:00:00Z"}][{"id":2,"body":"b","user":{"login":"y"},"created_at":"2025-01-02T00:00:00Z"}]`
	comments, err := parseComments(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	if comments[0].ID != 1 || comments[1].ID != 2 {
		t.Errorf("IDs = %d, %d, want 1, 2", comments[0].ID, comments[1].ID)
	}
}
