// Package vcs prepares review workspaces by cloning the pull request code.
package vcs

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// pullRefName is the local ref the PR head is fetched into.
func pullRefName(prNumber int) plumbing.ReferenceName {
	return plumbing.ReferenceName(fmt.Sprintf("refs/pull/%d/head", prNumber))
}

// PrepareWorkspace clones the pull request head of a repository into a fresh
// temporary directory and checks out the PR head commit, so the review tools
// operate on the exact code under review. The returned cleanup function
// removes the directory; it must be called when the review is done.
// cloneURL may be overridden in tests (e.g. a local repository path).
func PrepareWorkspace(ctx context.Context, token, cloneURL, owner, repoName string, prNumber int, headSHA string) (string, func(), error) {
	base, err := os.MkdirTemp("", "codereviewagent-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	fail := func(err error) (string, func(), error) {
		_ = os.RemoveAll(base)
		return "", nil, err
	}

	if cloneURL == "" {
		cloneURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, repoName)
	}
	auth := &http.BasicAuth{Username: "x-access-token", Password: token}

	repo, err := git.PlainCloneContext(ctx, base, false, &git.CloneOptions{
		URL:      cloneURL,
		Auth:     auth,
		Progress: nil,
	})
	if err != nil {
		return fail(fmt.Errorf("clone %s: %w", cloneURL, err))
	}

	// Fetch the PR head ref; the default clone only brings the default
	// branch, and pull refs are not part of the clone handshake.
	localRef := pullRefName(prNumber)
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec(fmt.Sprintf("%s:%s", localRef, localRef)),
		},
		Auth: auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		if headSHA == "" {
			return fail(fmt.Errorf("fetch pull ref %s: %w", localRef, err))
		}
		// 1) The commit may already be part of the cloned history (e.g. the
		// PR is built on the default branch).
		hash := plumbing.NewHash(headSHA)
		if _, oerr := repo.Object(plumbing.CommitObject, hash); oerr == nil {
			if err := checkoutHash(repo, hash); err != nil {
				return fail(err)
			}
			return base, func() { _ = os.RemoveAll(base) }, nil
		}
		// 2) Fetch the exact SHA from the remote (GitHub allows fetch by SHA).
		if fetched, ferr := fetchBySHA(ctx, repo, auth, headSHA); ferr != nil {
			return fail(fmt.Errorf("fetch pull ref %s (SHA fallback %s): %w", localRef, headSHA, ferr))
		} else if err := checkoutHash(repo, fetched); err != nil {
			return fail(err)
		}
		return base, func() { _ = os.RemoveAll(base) }, nil
	}

	ref, err := repo.Reference(localRef, false)
	if err != nil {
		return fail(fmt.Errorf("resolve pull ref %s: %w", localRef, err))
	}
	if err := checkoutHash(repo, ref.Hash()); err != nil {
		return fail(err)
	}
	return base, func() { _ = os.RemoveAll(base) }, nil
}

// fetchBySHA fetches a raw commit SHA into a temporary local ref and returns
// its hash.
func fetchBySHA(ctx context.Context, repo *git.Repository, auth *http.BasicAuth, headSHA string) (plumbing.Hash, error) {
	tmpRef := plumbing.ReferenceName("refs/codereviewagent/prhead")
	err := repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []gitconfig.RefSpec{
			gitconfig.RefSpec(fmt.Sprintf("%s:%s", headSHA, tmpRef)),
		},
		Auth: auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return plumbing.ZeroHash, err
	}
	ref, err := repo.Reference(tmpRef, false)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve SHA fallback ref: %w", err)
	}
	return ref.Hash(), nil
}

// checkoutHash checks out the given commit in the repository worktree.
func checkoutHash(repo *git.Repository, hash plumbing.Hash) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
		return fmt.Errorf("checkout %s: %w", hash, err)
	}
	return nil
}
