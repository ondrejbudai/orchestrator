package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/drellabot/orchestrator/internal/task"
)

func createTaskWithPRs(t *testing.T, outputDir, taskName string, prs []task.PR) {
	t.Helper()
	td, err := task.Create(outputDir, taskName)
	if err != nil {
		t.Fatal(err)
	}
	if err := td.SaveMetadata(taskName, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, pr := range prs {
		if err := td.AddPR(pr); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoverPRs(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		wantCount int
	}{
		{
			name:      "empty output dir",
			setup:     func(t *testing.T, dir string) {},
			wantCount: 0,
		},
		{
			name: "task with no PRs",
			setup: func(t *testing.T, dir string) {
				createTaskWithPRs(t, dir, "no-prs", nil)
			},
			wantCount: 0,
		},
		{
			name: "task with one open PR",
			setup: func(t *testing.T, dir string) {
				createTaskWithPRs(t, dir, "one-pr", []task.PR{
					{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "fix", Base: "main"},
				})
			},
			wantCount: 1,
		},
		{
			name: "task with closed PR is skipped",
			setup: func(t *testing.T, dir string) {
				td, err := task.Create(dir, "closed-pr")
				if err != nil {
					t.Fatal(err)
				}
				if err := td.SaveMetadata("closed-pr", "test", time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := td.AddPR(task.PR{
					URL: "https://github.com/org/repo/pull/1", Repo: "org/repo",
					Branch: "fix", Base: "main",
				}); err != nil {
					t.Fatal(err)
				}
				// Mark it closed
				if err := td.UpdatePR("https://github.com/org/repo/pull/1", func(pr *task.PR) {
					pr.Closed = true
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantCount: 0,
		},
		{
			name: "multiple tasks with PRs",
			setup: func(t *testing.T, dir string) {
				createTaskWithPRs(t, dir, "task-a", []task.PR{
					{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "a", Base: "main"},
				})
				createTaskWithPRs(t, dir, "task-b", []task.PR{
					{URL: "https://github.com/org/repo/pull/2", Repo: "org/repo", Branch: "b", Base: "main"},
					{URL: "https://github.com/org/repo/pull/3", Repo: "org/repo", Branch: "c", Base: "main"},
				})
			},
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			refs := DiscoverPRs(dir)
			if len(refs) != tt.wantCount {
				t.Errorf("got %d PRRefs, want %d", len(refs), tt.wantCount)
			}
		})
	}
}

func TestDiscoverPRs_PopulatesNumber(t *testing.T) {
	dir := t.TempDir()
	createTaskWithPRs(t, dir, "test-task", []task.PR{
		{URL: "https://github.com/org/repo/pull/42", Repo: "org/repo", Branch: "fix", Base: "main"},
	})

	refs := DiscoverPRs(dir)
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	if refs[0].PR.Number != 42 {
		t.Errorf("PR number = %d, want 42", refs[0].PR.Number)
	}
}

func TestFormatCommentsAsPrompt(t *testing.T) {
	tests := []struct {
		name     string
		comments []gh.Comment
		want     string
	}{
		{
			name: "single comment",
			comments: []gh.Comment{
				{ID: 1, Body: "Please fix this", User: gh.CommentUser{Login: "alice"}, CreatedAt: "2025-01-01T00:00:00Z", Type: gh.IssueComment},
			},
			want: "New PR comments:\n\n@alice at 2025-01-01T00:00:00Z:\n\nPlease fix this\n",
		},
		{
			name: "multiple comments with review",
			comments: []gh.Comment{
				{ID: 1, Body: "First", User: gh.CommentUser{Login: "alice"}, CreatedAt: "2025-01-01T00:00:00Z", Type: gh.IssueComment},
				{ID: 2, Body: "Nit here", User: gh.CommentUser{Login: "bob"}, CreatedAt: "2025-01-01T01:00:00Z", Type: gh.ReviewComment, Path: "main.go"},
			},
			want: "New PR comments:\n\n@alice at 2025-01-01T00:00:00Z:\n\nFirst\n\n---\n\n@bob at 2025-01-01T01:00:00Z on main.go:\n\nNit here\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommentsAsPrompt(tt.comments)
			if got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestFilterNewComments(t *testing.T) {
	comments := []gh.Comment{
		{ID: 10, Body: "old", User: gh.CommentUser{Login: "alice"}},
		{ID: 20, Body: "new from alice", User: gh.CommentUser{Login: "alice"}},
		{ID: 30, Body: "new from stranger", User: gh.CommentUser{Login: "stranger"}},
		{ID: 40, Body: "new from bob", User: gh.CommentUser{Login: "bob"}},
	}

	tests := []struct {
		name              string
		lastCommentID     int64
		allowedCommenters []string
		wantIDs           []int64
	}{
		{
			name:              "filter by ID and allowed users",
			lastCommentID:     15,
			allowedCommenters: []string{"alice", "bob"},
			wantIDs:           []int64{20, 40},
		},
		{
			name:              "all comments old",
			lastCommentID:     100,
			allowedCommenters: []string{"alice"},
			wantIDs:           nil,
		},
		{
			name:              "no allowed commenters",
			lastCommentID:     0,
			allowedCommenters: []string{},
			wantIDs:           nil,
		},
		{
			name:              "from zero baseline",
			lastCommentID:     0,
			allowedCommenters: []string{"alice", "bob"},
			wantIDs:           []int64{10, 20, 40},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterNewComments(comments, tt.lastCommentID, tt.allowedCommenters)
			var gotIDs []int64
			for _, c := range got {
				gotIDs = append(gotIDs, c.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Errorf("ID[%d] = %d, want %d", i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestProcessPR_SkipsRunningTask(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	dir := t.TempDir()
	createTaskWithPRs(t, dir, "running-task", []task.PR{
		{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "fix", Base: "main", Number: 1},
	})

	// writeArgCapture that says PR is open
	script := writeOpenPRScript(t)

	d := New(ghNew(script), time.Minute, "", dir, []string{"alice"})
	d.SetTaskRunning("running-task", true)

	ref := PRRef{
		TaskName:  "running-task",
		OutputDir: dir,
		PR:        task.PR{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Number: 1},
	}

	d.ProcessPR(context.Background(), ref)

	// Task should still be running (no new goroutine launched to clear it)
	if !d.IsTaskRunning("running-task") {
		t.Error("expected task to still be running")
	}
}

func TestProcessPR_SkipsClosedPR(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	dir := t.TempDir()
	createTaskWithPRs(t, dir, "closed-task", []task.PR{
		{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "fix", Base: "main", Number: 1},
	})

	// writeArgCapture that says PR is closed
	script := writeClosedPRScript(t)

	d := New(ghNew(script), time.Minute, "", dir, []string{"alice"})

	ref := PRRef{
		TaskName:  "closed-task",
		OutputDir: dir,
		PR:        task.PR{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Number: 1},
	}

	d.ProcessPR(context.Background(), ref)

	// Verify PR is marked closed in state
	td, err := task.Open(dir, "closed-task")
	if err != nil {
		t.Fatal(err)
	}
	state, err := td.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Resources.GitHub.PRs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(state.Resources.GitHub.PRs))
	}
	if !state.Resources.GitHub.PRs[0].Closed {
		t.Error("expected PR to be marked closed")
	}
}

func ghNew(bin string) *gh.Runner {
	return gh.New(bin)
}

// writeOpenPRScript creates a fake gh that returns "open" for PR state checks
// and empty arrays for comment fetches.
func writeOpenPRScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	content := `#!/bin/sh
# Check if this is a PR state check (has --jq)
for arg in "$@"; do
  if [ "$arg" = "--jq" ]; then
    printf 'open'
    exit 0
  fi
done
# Otherwise return empty comments
printf '[]'
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

func writeClosedPRScript(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	content := `#!/bin/sh
printf 'closed'
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}
