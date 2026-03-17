package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	gh "github.com/drellabot/orchestrator/internal/github"
	"github.com/drellabot/orchestrator/internal/task"
)

// PRRef ties a task name to one of its PRs.
type PRRef struct {
	TaskName  string
	OutputDir string
	PR        task.PR
}

// ContinueFunc is the function signature for launching task continue.
type ContinueFunc func(ctx context.Context, taskName, prompt string) error

// Daemon polls GitHub PRs for new comments and triggers task continue.
type Daemon struct {
	gh                *gh.Runner
	interval          time.Duration
	configPath        string
	outputDir         string
	allowedCommenters []string
	continueFunc      ContinueFunc
	mu                sync.Mutex
	running           map[string]bool
}

// New creates a Daemon.
func New(ghRunner *gh.Runner, interval time.Duration, configPath, outputDir string, allowedCommenters []string) *Daemon {
	d := &Daemon{
		gh:                ghRunner,
		interval:          interval,
		configPath:        configPath,
		outputDir:         outputDir,
		allowedCommenters: allowedCommenters,
		running:           make(map[string]bool),
	}
	d.continueFunc = d.defaultContinueFunc
	return d
}

// Run is the main polling loop. It discovers PRs, iterates through them
// round-robin, and re-discovers after each full cycle.
func (d *Daemon) Run(ctx context.Context) error {
	for {
		refs := DiscoverPRs(d.outputDir)
		if len(refs) == 0 {
			slog.Info("No open PRs found, waiting before re-discovery", "interval", d.interval)
			if err := sleep(ctx, d.interval); err != nil {
				return ctx.Err()
			}
			continue
		}

		slog.Info("Discovered PRs", "count", len(refs))

		perPR := d.interval / time.Duration(len(refs))
		const minInterval = 5 * time.Second
		if perPR < minInterval {
			perPR = minInterval
		}

		for _, ref := range refs {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			d.processPR(ctx, ref)

			if err := sleep(ctx, perPR); err != nil {
				return ctx.Err()
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// DiscoverPRs scans all task directories for state.json files with open PRs.
func DiscoverPRs(outputDir string) []PRRef {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		slog.Debug("Cannot read output dir", "dir", outputDir, "error", err)
		return nil
	}

	var refs []PRRef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		taskName := entry.Name()
		td, err := task.Open(outputDir, taskName)
		if err != nil {
			continue
		}
		state, err := td.LoadState()
		if err != nil {
			slog.Debug("Cannot load state", "task", taskName, "error", err)
			continue
		}
		for _, pr := range state.Resources.GitHub.PRs {
			if pr.Closed {
				continue
			}
			// Ensure Number is populated
			if pr.Number == 0 && pr.URL != "" {
				if n, err := task.PRNumberFromURL(pr.URL); err == nil {
					pr.Number = n
				}
			}
			refs = append(refs, PRRef{
				TaskName:  taskName,
				OutputDir: outputDir,
				PR:        pr,
			})
		}
	}
	return refs
}

func (d *Daemon) processPR(ctx context.Context, ref PRRef) {
	log := slog.With("task", ref.TaskName, "pr", ref.PR.URL)

	if ref.PR.Number == 0 {
		log.Warn("PR has no number, skipping")
		return
	}

	// Check if PR is still open
	open, err := d.gh.IsPROpen(ctx, ref.PR.Repo, ref.PR.Number)
	if err != nil {
		log.Warn("Failed to check PR state", "error", err)
		return
	}
	if !open {
		log.Info("PR is closed, marking as closed")
		td, err := task.Open(ref.OutputDir, ref.TaskName)
		if err != nil {
			log.Warn("Failed to open task dir", "error", err)
			return
		}
		if err := td.UpdatePR(ref.PR.URL, func(pr *task.PR) {
			pr.Closed = true
		}); err != nil {
			log.Warn("Failed to mark PR as closed", "error", err)
		}
		return
	}

	// Check if task is already running — hold the lock through to
	// setting running=true to avoid a TOCTOU race if ProcessPR is
	// ever called concurrently for the same task.
	d.mu.Lock()
	if d.running[ref.TaskName] {
		d.mu.Unlock()
		log.Debug("Task already running, skipping")
		return
	}
	d.mu.Unlock()

	// Fetch comments
	comments, err := d.gh.FetchAllComments(ctx, ref.PR.Repo, ref.PR.Number)
	if err != nil {
		log.Warn("Failed to fetch comments", "error", err)
		return
	}

	// Filter new comments
	newComments := FilterNewComments(comments, ref.PR.LastCommentID, d.allowedCommenters)
	if len(newComments) == 0 {
		log.Debug("No new comments")
		return
	}

	log.Info("Found new comments", "count", len(newComments))

	// Update LastCommentID before launching to avoid re-processing
	maxID := newComments[len(newComments)-1].ID
	td, err := task.Open(ref.OutputDir, ref.TaskName)
	if err != nil {
		log.Warn("Failed to open task dir", "error", err)
		return
	}
	if err := td.UpdatePR(ref.PR.URL, func(pr *task.PR) {
		pr.LastCommentID = maxID
	}); err != nil {
		log.Warn("Failed to update LastCommentID", "error", err)
		return
	}

	prompt := FormatCommentsAsPrompt(newComments)

	// Re-check and set running atomically — between the first check
	// above and here, another goroutine could have started a continue
	// for the same task (e.g. via concurrent ProcessPR calls).
	d.mu.Lock()
	if d.running[ref.TaskName] {
		d.mu.Unlock()
		log.Debug("Task became running during processing, skipping")
		return
	}
	d.running[ref.TaskName] = true
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.running, ref.TaskName)
			d.mu.Unlock()
		}()

		if err := d.continueFunc(ctx, ref.TaskName, prompt); err != nil {
			log.Error("task continue failed", "error", err)
		}
	}()
}

// FilterNewComments returns comments with ID > lastCommentID and user in
// the allowed list. The input must be sorted by ID.
func FilterNewComments(comments []gh.Comment, lastCommentID int64, allowedCommenters []string) []gh.Comment {
	allowed := make(map[string]bool, len(allowedCommenters))
	for _, u := range allowedCommenters {
		allowed[u] = true
	}

	var result []gh.Comment
	for _, c := range comments {
		if c.ID <= lastCommentID {
			continue
		}
		if !allowed[c.User.Login] {
			continue
		}
		result = append(result, c)
	}
	return result
}

// FormatCommentsAsPrompt formats comments as a chronological prompt.
func FormatCommentsAsPrompt(comments []gh.Comment) string {
	// Ensure sorted by ID
	sort.Slice(comments, func(i, j int) bool { return comments[i].ID < comments[j].ID })

	var sb strings.Builder
	sb.WriteString("New PR comments:\n\n")
	for i, c := range comments {
		if i > 0 {
			sb.WriteString("\n---\n\n")
		}
		header := fmt.Sprintf("@%s at %s", c.User.Login, c.CreatedAt)
		if c.Type == gh.ReviewComment && c.Path != "" {
			header += fmt.Sprintf(" on %s", c.Path)
		}
		sb.WriteString(header + ":\n\n")
		sb.WriteString(c.Body)
		sb.WriteString("\n")
	}
	return sb.String()
}

func (d *Daemon) defaultContinueFunc(ctx context.Context, taskName, prompt string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}

	args := []string{"task", "continue", "--config", d.configPath, taskName, prompt}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	slog.Info("Launching task continue", "task", taskName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("task continue %s: %w", taskName, err)
	}
	return nil
}

// ListTaskDirs returns the names of all task directories in outputDir.
func ListTaskDirs(outputDir string) ([]string, error) {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// WatchPR polls a single PR for new comments from allowed commenters.
// It returns the formatted prompt on the first new comment(s) found.
func WatchPR(ctx context.Context, ghRunner *gh.Runner, outputDir, taskName, prURL string, allowedCommenters []string, pollInterval time.Duration) (string, error) {
	td, err := task.Open(outputDir, taskName)
	if err != nil {
		return "", fmt.Errorf("opening task: %w", err)
	}

	state, err := td.LoadState()
	if err != nil {
		return "", fmt.Errorf("loading state: %w", err)
	}

	var pr *task.PR
	for i := range state.Resources.GitHub.PRs {
		if state.Resources.GitHub.PRs[i].URL == prURL {
			pr = &state.Resources.GitHub.PRs[i]
			break
		}
	}
	if pr == nil {
		return "", fmt.Errorf("PR %s not found in task %s", prURL, taskName)
	}

	prNumber := pr.Number
	if prNumber == 0 {
		n, err := task.PRNumberFromURL(prURL)
		if err != nil {
			return "", err
		}
		prNumber = n
	}

	baseline := pr.LastCommentID
	repo := pr.Repo

	for {
		comments, err := ghRunner.FetchAllComments(ctx, repo, prNumber)
		if err != nil {
			return "", fmt.Errorf("fetching comments: %w", err)
		}

		newComments := FilterNewComments(comments, baseline, allowedCommenters)
		if len(newComments) > 0 {
			return FormatCommentsAsPrompt(newComments), nil
		}

		if err := sleep(ctx, pollInterval); err != nil {
			return "", ctx.Err()
		}
	}
}

// IsTaskRunning reports whether the task is tracked as running.
func (d *Daemon) IsTaskRunning(taskName string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running[taskName]
}

// SetTaskRunning sets a task's running state (for testing).
func (d *Daemon) SetTaskRunning(taskName string, running bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if running {
		d.running[taskName] = true
	} else {
		delete(d.running, taskName)
	}
}

// SetContinueFunc overrides the function used to launch task continue (for testing).
func (d *Daemon) SetContinueFunc(fn ContinueFunc) {
	d.continueFunc = fn
}

// ProcessPR is an exported wrapper for testing.
func (d *Daemon) ProcessPR(ctx context.Context, ref PRRef) {
	d.processPR(ctx, ref)
}
