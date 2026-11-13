package github

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"codereviewagent/internal/review"
)

// FingerprintMarkerRe extracts the hidden finding fingerprint marker embedded
// in review comments (v1.11 FR-67).
var FingerprintMarkerRe = regexp.MustCompile(`<!--\s*cra-fp:([0-9a-f]{64})\s*-->`)

const reviewThreadsQuery = `query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      reviewThreads(first:100){
        nodes{
          id
          isResolved
          isOutdated
          comments(first:100){
            nodes{ path line body }
          }
        }
      }
    }
  }
}`

const resolveThreadMutation = `mutation($threadId:ID!){
  resolveReviewThread(input:{threadId:$threadId}){
    thread{ id isResolved }
  }
}`

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLError struct {
	Message string `json:"message"`
}

type reviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Comments   struct {
							Nodes []struct {
								Path string `json:"path"`
								Line int    `json:"line"`
								Body string `json:"body"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// ListReviewThreads reads the PR's review threads and parses the hidden
// fingerprint marker from the first comment of each thread (v1.11 FR-67).
func (c *Client) ListReviewThreads(ctx context.Context, owner, repo string, number int) ([]review.ThreadState, error) {
	body := graphQLRequest{
		Query: reviewThreadsQuery,
		Variables: map[string]any{
			"owner": owner, "repo": repo, "number": number,
		},
	}
	req, err := c.api.NewRequest(http.MethodPost, "graphql", body)
	if err != nil {
		return nil, fmt.Errorf("build review threads request: %w", err)
	}
	var resp reviewThreadsResponse
	if _, err := c.api.Do(ctx, req, &resp); err != nil {
		return nil, fmt.Errorf("read review threads %s/%s#%d: %w", owner, repo, number, err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("read review threads %s/%s#%d: %s", owner, repo, number, resp.Errors[0].Message)
	}

	nodes := resp.Data.Repository.PullRequest.ReviewThreads.Nodes
	threads := make([]review.ThreadState, 0, len(nodes))
	for _, node := range nodes {
		state := review.ThreadState{
			ThreadID:   node.ID,
			IsResolved: node.IsResolved,
			IsOutdated: node.IsOutdated,
		}
		for _, comment := range node.Comments.Nodes {
			if state.Path == "" {
				state.Path = comment.Path
				state.Line = comment.Line
			}
			if match := FingerprintMarkerRe.FindStringSubmatch(comment.Body); len(match) == 2 {
				state.Fingerprint = match[1]
				break
			}
		}
		threads = append(threads, state)
	}
	return threads, nil
}

type resolveThreadResponse struct {
	Data struct {
		ResolveReviewThread struct {
			Thread struct {
				ID         string `json:"id"`
				IsResolved bool   `json:"isResolved"`
			} `json:"thread"`
		} `json:"resolveReviewThread"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// ResolveReviewThread closes one review thread via GraphQL (v1.11 FR-68).
func (c *Client) ResolveReviewThread(ctx context.Context, threadID string) error {
	body := graphQLRequest{
		Query:     resolveThreadMutation,
		Variables: map[string]any{"threadId": threadID},
	}
	req, err := c.api.NewRequest(http.MethodPost, "graphql", body)
	if err != nil {
		return fmt.Errorf("build resolve thread request: %w", err)
	}
	var resp resolveThreadResponse
	if _, err := c.api.Do(ctx, req, &resp); err != nil {
		return fmt.Errorf("resolve review thread %s: %w", threadID, err)
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("resolve review thread %s: %s", threadID, resp.Errors[0].Message)
	}
	return nil
}
