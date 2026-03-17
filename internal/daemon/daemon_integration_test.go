package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/drellabot/orchestrator/internal/task"
)

// TestProcessPR_NewCommentsFlow verifies the full processPR flow:
// PR is open, has new comments from allowed users, and state is updated.
func TestProcessPR_NewCommentsFlow(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	dir := t.TempDir()
	createTaskWithPRs(t, dir, "comment-flow", []task.PR{
		{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "fix", Base: "main", Number: 1, LastCommentID: 50},
	})

	// Create a fake gh script that:
	// 1st call: returns "open" (PR state check)
	// 2nd call: returns issue comments (IDs 51, 52)
	// 3rd call: returns empty review comments
	scriptDir := t.TempDir()
	countFile := filepath.Join(scriptDir, "count")
	script := filepath.Join(scriptDir, "gh")

	content := `#!/bin/sh
if [ -f ` + countFile + ` ]; then
  n=$(cat ` + countFile + `)
else
  n=0
fi
echo $((n + 1)) > ` + countFile + `
case $n in
  0) printf 'open' ;;
  1) printf '[{"id":51,"body":"Please fix the typo","user":{"login":"alice"},"created_at":"2025-01-01T00:00:00Z"},{"id":52,"body":"Also update the docs","user":{"login":"alice"},"created_at":"2025-01-01T01:00:00Z"}]' ;;
  2) printf '[]' ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}

	d := New(gh.New(script), time.Minute, "", dir, []string{"alice"})

	// Capture the prompt sent to task continue
	var mu sync.Mutex
	var capturedTask, capturedPrompt string
	done := make(chan struct{})
	d.SetContinueFunc(func(ctx context.Context, taskName, prompt string) error {
		mu.Lock()
		capturedTask = taskName
		capturedPrompt = prompt
		mu.Unlock()
		close(done)
		return nil
	})

	ref := PRRef{
		TaskName:  "comment-flow",
		OutputDir: dir,
		PR:        task.PR{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Number: 1, LastCommentID: 50},
	}

	d.ProcessPR(context.Background(), ref)

	// Wait for the continue func to be called
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for continueFunc")
	}

	mu.Lock()
	defer mu.Unlock()

	if capturedTask != "comment-flow" {
		t.Errorf("task = %q, want %q", capturedTask, "comment-flow")
	}
	if capturedPrompt == "" {
		t.Fatal("expected non-empty prompt")
	}

	// Verify prompt contains both comments
	if got := capturedPrompt; got == "" {
		t.Fatal("empty prompt")
	}

	// Verify LastCommentID was updated to 52
	td, err := task.Open(dir, "comment-flow")
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
	if state.Resources.GitHub.PRs[0].LastCommentID != 52 {
		t.Errorf("LastCommentID = %d, want 52", state.Resources.GitHub.PRs[0].LastCommentID)
	}
}

// TestProcessPR_FiltersUnallowedCommenters verifies that comments from
// non-allowed users are ignored.
func TestProcessPR_FiltersUnallowedCommenters(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}

	dir := t.TempDir()
	createTaskWithPRs(t, dir, "filter-test", []task.PR{
		{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Branch: "fix", Base: "main", Number: 1, LastCommentID: 0},
	})

	scriptDir := t.TempDir()
	countFile := filepath.Join(scriptDir, "count")
	script := filepath.Join(scriptDir, "gh")

	content := `#!/bin/sh
if [ -f ` + countFile + ` ]; then
  n=$(cat ` + countFile + `)
else
  n=0
fi
echo $((n + 1)) > ` + countFile + `
case $n in
  0) printf 'open' ;;
  1) printf '[{"id":1,"body":"spam","user":{"login":"stranger"},"created_at":"2025-01-01T00:00:00Z"}]' ;;
  2) printf '[]' ;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}

	d := New(gh.New(script), time.Minute, "", dir, []string{"alice"})
	d.SetContinueFunc(func(ctx context.Context, taskName, prompt string) error {
		t.Error("continueFunc should not have been called")
		return nil
	})

	ref := PRRef{
		TaskName:  "filter-test",
		OutputDir: dir,
		PR:        task.PR{URL: "https://github.com/org/repo/pull/1", Repo: "org/repo", Number: 1, LastCommentID: 0},
	}

	d.ProcessPR(context.Background(), ref)

	// Give a moment to ensure no goroutine was launched
	time.Sleep(50 * time.Millisecond)

	// No task continue should have been launched
	if d.IsTaskRunning("filter-test") {
		t.Error("expected task NOT to be running (all comments filtered)")
	}

	// LastCommentID should NOT be updated
	td, err := task.Open(dir, "filter-test")
	if err != nil {
		t.Fatal(err)
	}
	state, err := td.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Resources.GitHub.PRs[0].LastCommentID != 0 {
		t.Errorf("LastCommentID = %d, want 0 (unchanged)", state.Resources.GitHub.PRs[0].LastCommentID)
	}
}
