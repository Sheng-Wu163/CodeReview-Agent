package handler

import (
	"encoding/json"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"codereviewagent/internal/repository"
)

// Dashboard serves the review history web pages and the JSON API (FR-15).
type Dashboard struct {
	repo       *repository.ReviewRepo
	feedback   *repository.FeedbackRepo // may be nil (v1.4 metrics off)
	logger     *zap.Logger
	listTmpl   *template.Template
	detailTmpl *template.Template
}

// listPageData feeds templates/dashboard.html.
type listPageData struct {
	Reviews []repository.Review
	Counts  map[int64]int
	// FeedbackStats feeds the v1.4 adoption-rate section (FR-43); empty
	// when the feedback repo is not wired or no findings exist yet.
	FeedbackStats []repository.AgentFeedbackStat
}

// detailPageData feeds templates/review_detail.html.
type detailPageData struct {
	Review   *repository.Review
	Findings []repository.Finding
}

// apiReview is the JSON shape of a review record.
type apiReview struct {
	ID            int64      `json:"id"`
	RepoFullName  string     `json:"repo_full_name"`
	PRNumber      int        `json:"pr_number"`
	HeadSHA       string     `json:"head_sha"`
	Status        string     `json:"status"`
	Summary       string     `json:"summary"`
	FindingsCount int        `json:"findings_count"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// apiFinding is the JSON shape of a finding record.
type apiFinding struct {
	ID         int64   `json:"id"`
	ReviewID   int64   `json:"review_id"`
	Agent      string  `json:"agent"`
	Severity   string  `json:"severity"`
	FilePath   string  `json:"file_path"`
	LineNumber int     `json:"line_number"`
	Message    string  `json:"message"`
	Suggestion string  `json:"suggestion"`
	Confidence float64 `json:"confidence"`
}

// tmplFuncs are the helper functions available to the dashboard templates.
var tmplFuncs = template.FuncMap{
	// mul100 converts a [0,1] ratio to a percentage for rendering.
	"mul100": func(v float64) float64 { return v * 100 },
}

// NewDashboard creates the dashboard handlers. templatesDir may be empty to
// use the project-root "templates" directory.
func NewDashboard(repo *repository.ReviewRepo, logger *zap.Logger, templatesDir string) (*Dashboard, error) {
	if templatesDir == "" {
		templatesDir = "templates"
	}
	listTmpl, err := template.New("").Funcs(tmplFuncs).ParseFiles(
		filepath.Join(templatesDir, "layout.html"),
		filepath.Join(templatesDir, "dashboard.html"),
	)
	if err != nil {
		return nil, err
	}
	detailTmpl, err := template.New("").Funcs(tmplFuncs).ParseFiles(
		filepath.Join(templatesDir, "layout.html"),
		filepath.Join(templatesDir, "review_detail.html"),
	)
	if err != nil {
		return nil, err
	}
	return &Dashboard{repo: repo, logger: logger, listTmpl: listTmpl, detailTmpl: detailTmpl}, nil
}

// SetFeedbackRepo wires the v1.4 implicit-feedback repository, enabling the
// adoption-rate metrics (FR-43). Optional.
func (d *Dashboard) SetFeedbackRepo(fb *repository.FeedbackRepo) {
	d.feedback = fb
}

// Register mounts the dashboard pages and JSON API routes on r.
func (d *Dashboard) Register(r chi.Router) {
	r.Get("/dashboard", d.handleList)
	r.Get("/dashboard/reviews/{id}", d.handleDetail)
	r.Get("/api/reviews", d.handleAPIList)
	r.Get("/api/reviews/{id}", d.handleAPIDetail)
	r.Get("/api/metrics/feedback", d.handleAPIMetricsFeedback)
}

// handleAPIMetricsFeedback serves GET /api/metrics/feedback (v1.4 FR-43):
// the per-agent adoption statistics (accept rate over resolved findings).
func (d *Dashboard) handleAPIMetricsFeedback(w http.ResponseWriter, r *http.Request) {
	if d.feedback == nil {
		http.Error(w, "feedback metrics not enabled", http.StatusNotImplemented)
		return
	}
	stats, err := d.feedback.FeedbackStats(r.Context())
	if err != nil {
		d.logger.Sugar().Errorw("api feedback stats", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type agentMetric struct {
		Agent      string  `json:"agent"`
		Total      int     `json:"total"`
		Accepted   int     `json:"accepted"`
		Ignored    int     `json:"ignored"`
		Pending    int     `json:"pending"`
		AcceptRate float64 `json:"accept_rate"`
	}
	out := make([]agentMetric, 0, len(stats))
	for _, s := range stats {
		out = append(out, agentMetric{
			Agent: s.Agent, Total: s.Total, Accepted: s.Accepted,
			Ignored: s.Ignored, Pending: s.Pending, AcceptRate: s.AcceptRate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

// handleList renders GET /dashboard (AC-19).
func (d *Dashboard) handleList(w http.ResponseWriter, r *http.Request) {
	reviews, err := d.repo.ListReviews(r.Context(), 100)
	if err != nil {
		d.logger.Sugar().Errorw("list reviews", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	counts := make(map[int64]int, len(reviews))
	for _, rev := range reviews {
		findings, err := d.repo.ListFindings(r.Context(), rev.ID)
		if err != nil {
			d.logger.Sugar().Errorw("list findings", "review", rev.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		counts[rev.ID] = len(findings)
	}
	var stats []repository.AgentFeedbackStat
	if d.feedback != nil {
		if stats, err = d.feedback.FeedbackStats(r.Context()); err != nil {
			d.logger.Sugar().Errorw("feedback stats", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	if err := d.listTmpl.ExecuteTemplate(w, "layout.html", listPageData{
		Reviews: reviews, Counts: counts, FeedbackStats: stats,
	}); err != nil {
		d.logger.Sugar().Errorw("render dashboard", "error", err)
	}
}

// handleDetail renders GET /dashboard/reviews/{id} (AC-20).
func (d *Dashboard) handleDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	rev, findings, ok := d.loadReview(w, r, id)
	if !ok {
		return
	}
	if err := d.detailTmpl.ExecuteTemplate(w, "layout.html", detailPageData{Review: rev, Findings: findings}); err != nil {
		d.logger.Sugar().Errorw("render review detail", "error", err)
	}
}

// handleAPIList serves GET /api/reviews (AC-18 list).
func (d *Dashboard) handleAPIList(w http.ResponseWriter, r *http.Request) {
	reviews, err := d.repo.ListReviews(r.Context(), 100)
	if err != nil {
		d.logger.Sugar().Errorw("api list reviews", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]apiReview, 0, len(reviews))
	for _, rev := range reviews {
		findings, err := d.repo.ListFindings(r.Context(), rev.ID)
		if err != nil {
			d.logger.Sugar().Errorw("api list findings", "review", rev.ID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out = append(out, toAPIReview(rev, len(findings)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": out})
}

// handleAPIDetail serves GET /api/reviews/{id} (AC-18 detail).
func (d *Dashboard) handleAPIDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	rev, findings, ok := d.loadReview(w, r, id)
	if !ok {
		return
	}
	out := make([]apiFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, apiFinding{
			ID: f.ID, ReviewID: f.ReviewID, Agent: f.Agent, Severity: f.Severity,
			FilePath: f.FilePath, LineNumber: f.LineNumber,
			Message: f.Message, Suggestion: f.Suggestion, Confidence: f.Confidence,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": toAPIReview(*rev, len(findings)), "findings": out})
}

// loadReview loads a review and its findings; it writes the HTTP error to w
// and returns ok=false on failure or when the review does not exist.
func (d *Dashboard) loadReview(w http.ResponseWriter, r *http.Request, id int64) (*repository.Review, []repository.Finding, bool) {
	rev, err := d.repo.GetReview(r.Context(), id)
	if err != nil {
		if err == pgx.ErrNoRows {
			http.Error(w, "review not found", http.StatusNotFound)
			return nil, nil, false
		}
		d.logger.Sugar().Errorw("get review", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, nil, false
	}
	findings, err := d.repo.ListFindings(r.Context(), id)
	if err != nil {
		d.logger.Sugar().Errorw("list findings", "review", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, nil, false
	}
	return rev, findings, true
}

// parseID extracts and validates the {id} path parameter.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid review id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// toAPIReview converts a stored review to its JSON shape.
func toAPIReview(rev repository.Review, findingsCount int) apiReview {
	return apiReview{
		ID: rev.ID, RepoFullName: rev.RepoFullName, PRNumber: rev.PRNumber,
		HeadSHA: rev.HeadSHA, Status: rev.Status, Summary: rev.Summary,
		FindingsCount: findingsCount,
		CreatedAt:     rev.CreatedAt, UpdatedAt: rev.UpdatedAt,
	}
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; nothing else we can do.
		return
	}
}
