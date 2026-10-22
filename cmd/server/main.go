// Command server is the entry point of the CodeReviewAgent HTTP service.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"codereviewagent/internal/ai"
	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/ai/tools"
	"codereviewagent/internal/config"
	"codereviewagent/internal/db"
	"codereviewagent/internal/feedback"
	"codereviewagent/internal/github"
	"codereviewagent/internal/handler"
	"codereviewagent/internal/middleware"
	"codereviewagent/internal/orchestrator"
	"codereviewagent/internal/repository"
	"codereviewagent/internal/review"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer func() { _ = logger.Sync() }()

	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	var (
		store      *repository.ReviewRepo
		reviewProc handler.PullRequestProcessor
	)

	// PostgreSQL is required for review persistence and the dashboard.
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required (review history and dashboard depend on it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := db.MigrateFromURL(ctx, cfg.DatabaseURL)
	cancel()
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	store = repository.NewReviewRepo(pool)
	dashboard, err := handler.NewDashboard(store, logger, "templates")
	if err != nil {
		return fmt.Errorf("create dashboard: %w", err)
	}
	dashboard.SetFeedbackRepo(repository.NewFeedbackRepo(pool))
	dashboard.Register(r)

	// Full review pipeline: LLM + 4 review agents + verifier + orchestrator.
	llm, err := ai.NewLLM(cfg.LLMProvider, apiKeyFor(cfg))
	if err != nil {
		return fmt.Errorf("init LLM: %w", err)
	}
	reviewAgents := []agents.ReviewAgent{
		agents.NewSecurityAgent(llm),
		agents.NewLogicAgent(llm),
		agents.NewPerformanceAgent(llm),
		agents.NewLintAgent(llm),
	}
	orch := orchestrator.New(logger, "", reviewAgents,
		func(root string) orchestrator.Verifier {
			return agents.NewVerifier(tools.ReadFileTool{Root: root}, nil)
		}, nil)
	orch.SetAnalysisConfig(cfg.StaticAnalysis)
	orch.SetCompressionConfig(cfg.Compression)
	orch.SetCheckpointStore(repository.NewCheckpointRepo(pool))
	orch.SetCheckpointConfig(cfg.ForceRerun, cfg.ForceRerunAgents)
	orch.SetReviewStrategy(review.NewAssessor(review.AssessorOptions{
		TrivialMaxLines: cfg.StaticAnalysis.Adaptive.TrivialMaxLines,
		ComplexMinLines: cfg.StaticAnalysis.Adaptive.ComplexMinLines,
		ComplexMinFiles: cfg.StaticAnalysis.Adaptive.ComplexMinFiles,
		SensitivePaths:  cfg.StaticAnalysis.Adaptive.SensitivePaths,
	}), review.NewSelector())
	ghClient := github.NewClient(cfg.GitHubToken)
	pipeline := orchestrator.NewPipeline(orch, ghClient, store, cfg.GitHubToken, logger)
	reviewProc = eventProcessorAdapter{pipeline: pipeline}

	// v1.4: implicit feedback tracking over synchronize/closed events.
	feedbackRepo := repository.NewFeedbackRepo(pool)
	var feedbackTracker handler.FeedbackTracker = feedback.NewTracker(feedbackRepo, ghClient, cfg.Feedback.SimilarityThreshold, logger)

	// v1.7: SSE review stream - the orchestrator's lifecycle events fan out
	// through an in-process broker, keyed by "<repo>#<pr>".
	eventBroker := handler.NewEventBroker()
	eventCh := make(chan orchestrator.ReviewEvent, 256)
	orch.SetEventChan(eventCh)
	go func() {
		for ev := range eventCh {
			routeEvent(eventBroker, ev)
		}
	}()
	handler.MountStreamReview(r, eventBroker, logger)
	handler.MountStatic(r, "./static")

	r.With(middleware.HMACVerify(cfg.WebhookSecret)).
		Post("/webhook", handler.NewWebhook(logger, reviewProc, feedbackTracker).ServeHTTP)

	srv := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: r,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Sugar().Infof("server started on :%s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen and serve: %w", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
	}
	return nil
}

// apiKeyFor returns the API key matching the configured LLM provider.
func apiKeyFor(cfg *config.Config) string {
	if cfg.LLMProvider == ai.ProviderAnthropic {
		return cfg.AnthropicAPIKey
	}
	return cfg.OpenAIAPIKey
}

// routeEvent publishes a review-stream event under its topic "<repo>#<pr>"
// (v1.7).
func routeEvent(broker *handler.EventBroker, ev orchestrator.ReviewEvent) {
	repo, _ := ev.Data["repo_full_name"].(string)
	if repo == "" {
		return
	}
	broker.Publish(fmt.Sprintf("%s#%v", repo, ev.Data["pr_number"]), ev)
}

// eventProcessorAdapter adapts handler.PullRequestEvent to the pipeline's
// EventInput (keeps the two packages decoupled).
type eventProcessorAdapter struct {
	pipeline *orchestrator.Pipeline
}

// Process implements handler.PullRequestProcessor.
func (a eventProcessorAdapter) Process(ctx context.Context, ev handler.PullRequestEvent) {
	a.pipeline.Process(ctx, orchestrator.EventInput{
		RepoFullName: ev.RepoFullName,
		PRNumber:     ev.Number,
		HeadSHA:      ev.HeadSHA,
	})
}
