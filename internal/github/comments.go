package github

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CommentType distinguishes top-level from review comments.
type CommentType string

const (
	IssueComment  CommentType = "issue"
	ReviewComment CommentType = "review"
)

// Comment is a unified representation of both issue-level and
// review-level PR comments.
type Comment struct {
	ID        int64       `json:"id"`
	Body      string      `json:"body"`
	User      CommentUser `json:"user"`
	CreatedAt string      `json:"created_at"`
	Type      CommentType `json:"-"`

	// Review comment fields (only set for review comments)
	Path     string `json:"path,omitempty"`
	DiffHunk string `json:"diff_hunk,omitempty"`
}

// CommentUser holds the login of the comment author.
type CommentUser struct {
	Login string `json:"login"`
}

// ListIssueComments fetches top-level conversation comments on a PR.
func (r *Runner) ListIssueComments(ctx context.Context, repo string, prNumber int) ([]Comment, error) {
	out, err := r.run(ctx, "", r.bin, "api", "--paginate",
		fmt.Sprintf("/repos/%s/issues/%d/comments", repo, prNumber))
	if err != nil {
		return nil, fmt.Errorf("listing issue comments: %w", err)
	}
	comments, err := parseComments(out)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		comments[i].Type = IssueComment
	}
	return comments, nil
}

// ListReviewComments fetches line-level review comments on a PR.
func (r *Runner) ListReviewComments(ctx context.Context, repo string, prNumber int) ([]Comment, error) {
	out, err := r.run(ctx, "", r.bin, "api", "--paginate",
		fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, prNumber))
	if err != nil {
		return nil, fmt.Errorf("listing review comments: %w", err)
	}
	comments, err := parseComments(out)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		comments[i].Type = ReviewComment
	}
	return comments, nil
}

// IsPROpen checks whether a PR is still open.
func (r *Runner) IsPROpen(ctx context.Context, repo string, prNumber int) (bool, error) {
	out, err := r.run(ctx, "", r.bin, "api",
		fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber),
		"--jq", ".state")
	if err != nil {
		return false, fmt.Errorf("checking PR state: %w", err)
	}
	return strings.TrimSpace(out) == "open", nil
}

// FetchAllComments retrieves both issue and review comments, merges them,
// and returns the result sorted by ID.
func (r *Runner) FetchAllComments(ctx context.Context, repo string, prNumber int) ([]Comment, error) {
	issue, err := r.ListIssueComments(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	review, err := r.ListReviewComments(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	all := append(issue, review...)
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}

// parseComments handles gh api --paginate output, which may concatenate
// multiple JSON arrays (one per page).
func parseComments(out string) ([]Comment, error) {
	out = strings.TrimSpace(out)
	if out == "" || out == "[]" {
		return nil, nil
	}

	// gh api --paginate concatenates JSON arrays, e.g. "[...][...]"
	// Use a JSON decoder to handle this.
	var all []Comment
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var page []Comment
		if err := dec.Decode(&page); err != nil {
			return nil, fmt.Errorf("parsing comments JSON: %w", err)
		}
		all = append(all, page...)
	}
	return all, nil
}
