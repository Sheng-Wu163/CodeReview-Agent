// Package github wraps the GitHub API operations used by the review
// pipeline (FR-12): fetching PR diffs, submitting PR reviews and updating
// commit statuses.
package github

import (
	"context"
	"fmt"
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
