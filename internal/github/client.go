// Package github wraps the GitHub API operations used by the review
// pipeline (FR-12): fetching PR diffs, submitting PR reviews and updating
// commit statuses.
package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	gogithub "github.com/google/go-github/v66/github"
)

// Client wraps a go-github client for the operations CodeReviewAgent needs.
type Client struct {
	api *gogithub.Client
}

// NewClient creates a GitHub API client authenticated with the given token.
func NewClient(token string) *Client {
	api := gogithub.NewClient(nil).WithAuthToken(token)
	return &Client{api: api}
}

// NewClientWithEndpoint creates a client pointing at a custom API endpoint
// (used by tests to target a mock server, and by self-hosted deployments
// against GitHub Enterprise). baseURL must end with "/".
func NewClientWithEndpoint(token, baseURL string) *Client {
	api := gogithub.NewClient(nil).WithAuthToken(token)
	if parsed, err := url.Parse(baseURL); err == nil && parsed != nil {
		api.BaseURL = parsed
	}
	return &Client{api: api}
}

// GetPRDiff fetches the raw unified diff of a pull request (FR-03 input).
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	diffText, _, err := c.api.PullRequests.GetRaw(ctx, owner, repo, number, gogithub.RawOptions{
		Type: gogithub.Diff,
	})
	if err != nil {
		return "", fmt.Errorf("get PR diff %s/%s#%d: %w", owner, repo, number, err)
	}
	return diffText, nil
}

// GetPRHeadSHA returns the head commit SHA of a pull request (FR-02).
func (c *Client) GetPRHeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	pr, _, err := c.api.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("get PR %s/%s#%d: %w", owner, repo, number, err)
	}
	if pr.GetHead() == nil || pr.GetHead().GetSHA() == "" {
		return "", fmt.Errorf("PR %s/%s#%d has no head SHA", owner, repo, number)
	}
	return pr.GetHead().GetSHA(), nil
}

// ErrFileNotFound is returned by GetFileAtRef when the file does not exist
// at the requested ref (e.g. it was deleted by a later commit) — the
// implicit feedback tracker treats this as "file deleted" (v1.4 spec 15.4).
var ErrFileNotFound = errors.New("file not found at ref")

// GetFileAtRef fetches the content of one file at the given ref (a commit
// SHA, branch or tag). Used by the implicit feedback tracker (v1.4 FR-41);
// callers must not implement raw HTTP calls themselves (CLAUDE.v.1.4.md).
func (c *Client) GetFileAtRef(ctx context.Context, owner, repo, path, ref string) (string, error) {
	r, resp, err := c.api.Repositories.DownloadContents(ctx, owner, repo, path, &gogithub.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("%w: %s@%s", ErrFileNotFound, path, ref)
		}
		return "", fmt.Errorf("download %s at %s (%s/%s): %w", path, ref, owner, repo, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read %s at %s (%s/%s): %w", path, ref, owner, repo, err)
	}
	return string(data), nil
}
