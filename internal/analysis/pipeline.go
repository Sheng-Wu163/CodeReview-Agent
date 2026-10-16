package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"codereviewagent/internal/config"
	"codereviewagent/internal/diff"
)

// Pipeline defaults (FR-19).
const (
	// DefaultWorkers is the worker count used when the config omits it.
	DefaultWorkers = 4
	// DefaultPerFileTimeout bounds each tool run on each file.
	DefaultPerFileTimeout = 30 * time.Second
	// maxFileLines is the line limit above which static analysis is skipped
	// (spec 11.5 edge case: files over 5000 lines).
	maxFileLines = 5000
)

// ToolFailure records one analyzer failure (FR-20 degradation).
type ToolFailure struct {
	// Tool is the analyzer that failed.
	Tool string
	// File is the file it failed on.
	File string
	// Err is the failure reason.
	Err string
}

// SkippedFile records a file that did not get analyzed.
type SkippedFile struct {
	// Path is the file path.
	Path string
	// Reason explains why the file was skipped.
	Reason string
}

// Result is the pipeline output: findings grouped by file path, plus
// degradation bookkeeping.
type Result struct {
	// ByFile maps file path to its findings (deduplicated, ordered by line).
	ByFile map[string][]PreAnalysisFinding
	// Failures lists per tool/file failures that did not stop the pipeline.
	Failures []ToolFailure
	// Skipped lists files excluded from analysis (too large, deleted...).
	Skipped []SkippedFile
	// Duration is the total wall time (observability).
	Duration time.Duration
}

// Pipeline runs all enabled analyzers over all changed files with bounded
// concurrency and per-file timeouts (FR-17, FR-19, FR-20).
type Pipeline struct {
	logger *zap.Logger
	// root is the review workspace the analyzed files live in.
	root string
	// analyzers are the enabled analyzer instances.
	analyzers []Analyzer
	// workers bounds concurrent analyzer executions.
	workers int
	// perFileTimeout bounds one tool run on one file.
	perFileTimeout time.Duration
}

// NewPipeline builds the pipeline from configuration. cfg may be the zero
// value: disabled toggles and empty fields are normalized to the defaults.
func NewPipeline(logger *zap.Logger, root string, cfg config.StaticAnalysisConfig) *Pipeline {
	p := &Pipeline{
		logger:  logger,
		root:    root,
		workers: cfg.Workers,
	}
	if p.workers <= 0 {
		p.workers = DefaultWorkers
	}
	p.perFileTimeout = time.Duration(cfg.PerFileTimeoutSeconds) * time.Second
	if p.perFileTimeout <= 0 {
		p.perFileTimeout = DefaultPerFileTimeout
	}
	if !cfg.Enabled {
		return p // no analyzers: agents run pure LLM review
	}

	if cfg.Tools.Gitleaks.Enabled {
		extra := make([]SecretRule, 0, len(cfg.Tools.Gitleaks.CustomRules))
		for _, r := range cfg.Tools.Gitleaks.CustomRules {
			extra = append(extra, SecretRule{
				ID: r.ID, Description: r.Description,
				Pattern: r.Pattern, Keywords: r.Keywords,
			})
		}
		ga, err := NewGitleaksAnalyzer(root, extra)
		if err != nil {
			// Gitleaks is embedded; a failure here is a build error
			// (FR-20: critical), but the pipeline still degrades.
			logger.Sugar().Errorw("gitleaks analyzer unavailable", "error", err)
		} else {
			p.analyzers = append(p.analyzers, ga)
		}
	}
	if cfg.Tools.GolangciLint.Enabled {
		p.analyzers = append(p.analyzers,
			NewGolangciLintAnalyzer(root, cfg.GolangciConfigPath))
	}
	if cfg.Tools.Semgrep.Enabled {
		p.analyzers = append(p.analyzers,
			NewSemgrepAnalyzer(root, cfg.SemgrepRulesDir))
	}
	return p
}

// Run analyzes all changed files with all enabled analyzers. It merges and
// deduplicates results by fingerprint and groups them by file path. Per FR-20
// individual tool failures are recorded and do not fail the run; only
// context cancellation (or an empty analyzer set with strict=true) errors.
func (p *Pipeline) Run(ctx context.Context, files []diff.FileDiff) (*Result, error) {
	start := time.Now()
	res := &Result{ByFile: make(map[string][]PreAnalysisFinding)}

	// Deduplicate files by path: a file modified multiple times in one PR is
	// analyzed once, on its final version (spec 11.5).
	unique := make(map[string]diff.FileDiff, len(files))
	order := make([]string, 0, len(files))
	for _, f := range files {
		path := f.Path
		if path == "" {
			// Deleted files have no content to analyze.
			res.Skipped = append(res.Skipped, SkippedFile{
				Path: f.OldPath, Reason: "deleted file",
			})
			continue
		}
		if _, ok := unique[path]; ok {
			continue
		}
		unique[path] = f
		order = append(order, path)
	}

	// Skip oversized files (spec 11.5: > 5000 lines).
	jobs := make(chan struct {
		a Analyzer
		f diff.FileDiff
	}, len(p.analyzers)*len(order))
	for _, path := range order {
		lines, err := countLines(filepath.Join(p.root, path))
		if err != nil {
			res.Skipped = append(res.Skipped, SkippedFile{Path: path, Reason: "unreadable"})
			delete(unique, path)
			continue
		}
		if lines > maxFileLines {
			p.logger.Sugar().Warnw("file too large, skipping static analysis",
				"path", path, "lines", lines)
			res.Skipped = append(res.Skipped, SkippedFile{Path: path, Reason: "file too large"})
			delete(unique, path)
			continue
		}
	}

	var mu sync.Mutex
	var seen sync.Map // fingerprint -> true (dedup)

	g := new(errgroup.Group)
	// Producer.
	g.Go(func() error {
		defer close(jobs)
		for _, path := range order {
			f, ok := unique[path]
			if !ok {
				continue
			}
			for _, a := range p.analyzers {
				jobs <- struct {
					a Analyzer
					f diff.FileDiff
				}{a, f}
			}
		}
		return nil
	})
	// Workers (FR-19: bounded concurrency).
	for i := 0; i < p.workers; i++ {
		g.Go(func() error {
			for job := range jobs {
				p.runJob(ctx, job.a, job.f, res, &mu, &seen)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("analysis pipeline: %w", err)
	}

	// Order findings within each file by line number for stable prompts.
	for path, group := range res.ByFile {
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Line != group[j].Line {
				return group[i].Line < group[j].Line
			}
			if group[i].Tool != group[j].Tool {
				return group[i].Tool < group[j].Tool
			}
			return group[i].RuleID < group[j].RuleID
		})
		res.ByFile[path] = group
	}

	res.Duration = time.Since(start)
	return res, nil
}

// runJob runs one analyzer on one file under the per-file timeout, recording
// findings or the failure (FR-20). It never returns an error: failures are
// recorded in res. The timeout is enforced even for analyzers that ignore
// ctx (a hanging CLI is abandoned in its goroutine).
func (p *Pipeline) runJob(ctx context.Context, a Analyzer, f diff.FileDiff, res *Result, mu *sync.Mutex, seen *sync.Map) {
	jobCtx, cancel := context.WithTimeout(ctx, p.perFileTimeout)
	defer cancel()

	type analyzeResult struct {
		findings []PreAnalysisFinding
		err      error
	}
	done := make(chan analyzeResult, 1)
	go func() {
		findings, err := a.Analyze(jobCtx, f)
		done <- analyzeResult{findings: findings, err: err}
	}()

	var findings []PreAnalysisFinding
	var err error
	select {
	case r := <-done:
		findings, err = r.findings, r.err
	case <-jobCtx.Done():
		err = fmt.Errorf("timeout after %s: %w", p.perFileTimeout, jobCtx.Err())
	}

	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		res.Failures = append(res.Failures, ToolFailure{
			Tool: a.Name(), File: f.Path, Err: err.Error(),
		})
		if ctx.Err() != nil || jobCtx.Err() != nil {
			p.logger.Sugar().Warnw("static analysis timeout",
				"tool", a.Name(), "file", f.Path)
		} else if a.Name() == ToolGitleaks {
			// FR-20: gitleaks being unavailable is critical (it is
			// embedded); other tools degrade with a warning.
			p.logger.Sugar().Errorw("secret scanner failed",
				"file", f.Path, "error", err)
		} else {
			p.logger.Sugar().Warnw("static analyzer failed, degrading",
				"tool", a.Name(), "file", f.Path, "error", err)
		}
		return
	}
	added := 0
	for _, fnd := range findings {
		if _, dup := seen.LoadOrStore(fnd.ID, true); dup {
			continue // same fingerprint: duplicate finding, drop (FR-16)
		}
		res.ByFile[f.Path] = append(res.ByFile[f.Path], fnd)
		added++
	}
	p.logger.Sugar().Debugw("analyzer finished",
		"tool", a.Name(), "file", f.Path, "findings", added)
}

// countLines counts the number of lines in a file. Missing files are an
// error so callers can mark them unreadable.
func countLines(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	return strings.Count(string(data), "\n"), nil
}
