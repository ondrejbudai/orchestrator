package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PR records a pull request opened by the task.
type PR struct {
	URL           string `json:"url"`
	Repo          string `json:"repo"`
	Branch        string `json:"branch"`
	Base          string `json:"base"`
	Number        int    `json:"number,omitempty"`
	LastCommentID int64  `json:"last_comment_id,omitempty"`
	Closed        bool   `json:"closed,omitempty"`
}

// PRNumberFromURL extracts the pull request number from a GitHub PR URL
// of the form https://github.com/owner/repo/pull/42.
func PRNumberFromURL(url string) (int, error) {
	// Expected format: https://github.com/owner/repo/pull/NUMBER
	const prefix = "/pull/"
	idx := strings.LastIndex(url, prefix)
	if idx == -1 {
		return 0, fmt.Errorf("URL does not contain /pull/: %s", url)
	}
	numStr := url[idx+len(prefix):]
	// Strip any trailing path components
	if i := strings.Index(numStr, "/"); i != -1 {
		numStr = numStr[:i]
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid PR number in URL %s: %w", url, err)
	}
	return n, nil
}

// GitHubResources holds GitHub-related resources created by the task.
type GitHubResources struct {
	PRs []PR `json:"prs"`
}

// Resources holds external resources created by the task.
type Resources struct {
	GitHub GitHubResources `json:"github"`
}

// State holds task metadata and mutable state persisted to state.json.
type State struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	Resources   Resources `json:"resources"`
}

// Dir represents a per-task directory structure.
type Dir struct {
	root string
	mu   sync.Mutex
}

// Create creates a new task directory structure under outputDir.
// It fails if the task directory already exists.
func Create(outputDir, taskName string) (*Dir, error) {
	root := filepath.Join(outputDir, taskName)
	if _, err := os.Stat(root); err == nil {
		return nil, fmt.Errorf("task directory already exists: %s", root)
	}

	for _, sub := range []string{"repo", "conversations"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0755); err != nil {
			return nil, fmt.Errorf("creating task directory: %w", err)
		}
	}

	return &Dir{root: root}, nil
}

// Open returns a Dir for an existing task directory.
// It fails if the task directory does not exist.
func Open(outputDir, taskName string) (*Dir, error) {
	root := filepath.Join(outputDir, taskName)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("task directory does not exist: %s", root)
	}
	return &Dir{root: root}, nil
}

// RepoPath returns the path to the repo subdirectory.
func (d *Dir) RepoPath() string {
	return filepath.Join(d.root, "repo")
}

// ConversationsPath returns the path to the conversations subdirectory.
func (d *Dir) ConversationsPath() string {
	return filepath.Join(d.root, "conversations")
}

// TranscriptPath returns the path to the stream-json transcript file.
func (d *Dir) TranscriptPath() string {
	return filepath.Join(d.root, "transcript.jsonl")
}

// TranscriptPathFor returns the transcript path for a task by name,
// without requiring a Dir instance.
func TranscriptPathFor(outputDir, taskName string) string {
	return filepath.Join(outputDir, taskName, "transcript.jsonl")
}

func (d *Dir) statePath() string {
	return filepath.Join(d.root, "state.json")
}

// LoadState reads the task state from disk. Returns an empty State if
// the file does not exist yet.
func (d *Dir) LoadState() (*State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.loadStateLocked()
}

func (d *Dir) loadStateLocked() (*State, error) {
	data, err := os.ReadFile(d.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshaling state: %w", err)
	}
	return &s, nil
}

func (d *Dir) saveStateLocked(s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	return os.WriteFile(d.statePath(), data, 0644)
}

// SaveMetadata writes task metadata to state.json.
func (d *Dir) SaveMetadata(name, description string, createdAt time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, err := d.loadStateLocked()
	if err != nil {
		return err
	}
	s.Name = name
	s.Description = description
	s.CreatedAt = createdAt
	return d.saveStateLocked(s)
}

// AddPR appends a PR to the task state and persists it to disk.
// It automatically populates the Number field from the URL if not set.
func (d *Dir) AddPR(pr PR) error {
	if pr.Number == 0 && pr.URL != "" {
		if n, err := PRNumberFromURL(pr.URL); err == nil {
			pr.Number = n
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	s, err := d.loadStateLocked()
	if err != nil {
		return err
	}
	s.Resources.GitHub.PRs = append(s.Resources.GitHub.PRs, pr)
	return d.saveStateLocked(s)
}

// UpdatePR finds the PR with the given URL and applies the mutation function.
// Returns an error if the PR is not found.
func (d *Dir) UpdatePR(prURL string, fn func(*PR)) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	s, err := d.loadStateLocked()
	if err != nil {
		return err
	}
	for i := range s.Resources.GitHub.PRs {
		if s.Resources.GitHub.PRs[i].URL == prURL {
			fn(&s.Resources.GitHub.PRs[i])
			return d.saveStateLocked(s)
		}
	}
	return fmt.Errorf("PR not found: %s", prURL)
}
