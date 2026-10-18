// Package config provides application configuration loading via Viper.
// Configuration values are read from a YAML file and overridable by
// environment variables.
package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config holds all runtime configuration for the application.
type Config struct {
	// ServerPort is the HTTP listen port (default "8080").
	ServerPort string `mapstructure:"server_port"`
	// WebhookSecret is the GitHub webhook HMAC secret.
	WebhookSecret string `mapstructure:"webhook_secret"`
	// GitHubToken is the GitHub App token used to call the GitHub API.
	GitHubToken string `mapstructure:"github_token"`
	// DatabaseURL is the PostgreSQL connection string.
	DatabaseURL string `mapstructure:"database_url"`
	// LLMProvider is either "openai" or "anthropic".
	LLMProvider string `mapstructure:"llm_provider"`
	// OpenAIAPIKey is the OpenAI API key.
	OpenAIAPIKey string `mapstructure:"openai_api_key"`
	// AnthropicAPIKey is the Anthropic API key.
	AnthropicAPIKey string `mapstructure:"anthropic_api_key"`
	// Filter holds the file exclusion rules used before review (FR-04).
	Filter FilterConfig `mapstructure:"filter"`
	// StaticAnalysis configures the pre-analysis pipeline (FR-24).
	StaticAnalysis StaticAnalysisConfig `mapstructure:"static_analysis"`
}

// FilterConfig configures which files are excluded from review.
type FilterConfig struct {
	// ExcludeDirs lists directory names whose contents are skipped, e.g.
	// "vendor". Matched against any path segment.
	ExcludeDirs []string `mapstructure:"exclude_dirs"`
	// ExcludePatterns lists glob patterns (path.Match) matched against the
	// full path and the base file name, e.g. "*.md", "go.sum".
	ExcludePatterns []string `mapstructure:"exclude_patterns"`
}

// StaticAnalysisConfig configures the pre-review static analysis pipeline
// (FR-24). Every knob is controllable from config.yaml.
type StaticAnalysisConfig struct {
	// Enabled turns the whole pipeline on/off. When disabled, agents run
	// without preFindings (pure LLM review).
	Enabled bool `mapstructure:"enabled"`
	// Workers is the max number of concurrent analyzer executions (FR-19,
	// default 4).
	Workers int `mapstructure:"workers"`
	// PerFileTimeoutSeconds bounds each tool run on each file (FR-19,
	// default 30).
	PerFileTimeoutSeconds int `mapstructure:"per_file_timeout_seconds"`
	// SemgrepRulesDir is the custom semgrep rule directory, relative to the
	// review workspace.
	SemgrepRulesDir string `mapstructure:"semgrep_rules_dir"`
	// GolangciConfigPath optionally points to a golangci-lint config file.
	GolangciConfigPath string `mapstructure:"golangci_config_path"`
	// Tools toggles the individual analyzers (FR-20 degradation).
	Tools StaticAnalysisTools `mapstructure:"tools"`
	// Adaptive configures the v1.2 complexity-based review strategy
	// (FR-38).
	Adaptive AdaptiveConfig `mapstructure:"adaptive"`
}

// AdaptiveConfig holds the deterministic complexity thresholds of the v1.2
// adaptive review strategy (FR-34/FR-38). Zero/empty values fall back to the
// built-in defaults in internal/review.
type AdaptiveConfig struct {
	// TrivialMaxLines is the exclusive upper line bound for Trivial.
	TrivialMaxLines int `mapstructure:"trivial_max_lines"`
	// ComplexMinLines is the exclusive lower line bound for Complex.
	ComplexMinLines int `mapstructure:"complex_min_lines"`
	// ComplexMinFiles is the exclusive file-count bound for Complex.
	ComplexMinFiles int `mapstructure:"complex_min_files"`
	// SensitivePaths force the Complex level; empty uses built-in defaults.
	SensitivePaths []string `mapstructure:"sensitive_paths"`
}

// StaticAnalysisTools holds per-tool enable switches.
type StaticAnalysisTools struct {
	// Semgrep toggles the semgrep security scanner.
	Semgrep StaticToolToggle `mapstructure:"semgrep"`
	// GolangciLint toggles the golangci-lint quality scanner.
	GolangciLint StaticToolToggle `mapstructure:"golangci_lint"`
	// Gitleaks toggles the embedded secret scanner.
	Gitleaks StaticGitleaksConfig `mapstructure:"gitleaks"`
}

// StaticToolToggle is a simple enable switch.
type StaticToolToggle struct {
	// Enabled runs the tool when true.
	Enabled bool `mapstructure:"enabled"`
}

// StaticGitleaksConfig configures the secret scanner including custom rules
// (FR-18).
type StaticGitleaksConfig struct {
	// Enabled runs the secret scanner when true.
	Enabled bool `mapstructure:"enabled"`
	// CustomRules extend the gitleaks default ruleset.
	CustomRules []StaticGitleaksRule `mapstructure:"custom_rules"`
}

// StaticGitleaksRule is one user-defined secret rule.
type StaticGitleaksRule struct {
	// ID is the rule identifier reported in findings.
	ID string `mapstructure:"id"`
	// Description is a human-readable explanation.
	Description string `mapstructure:"description"`
	// Pattern is the regular expression matching the secret.
	Pattern string `mapstructure:"pattern"`
	// Keywords are quick pre-filter strings.
	Keywords []string `mapstructure:"keywords"`
}

// Load reads configuration from the optional YAML file at configPath and
// applies environment variable overrides. Environment variables follow the
// names documented in CLAUDE.md (e.g. WEBHOOK_SECRET, SERVER_PORT).
func Load(configPath string) (*Config, error) {
	v := viper.New()

	// Defaults.
	v.SetDefault("server_port", "8080")
	v.SetDefault("llm_provider", "openai")
	v.SetDefault("filter.exclude_dirs", []string{"vendor", "third_party", "node_modules"})
	v.SetDefault("filter.exclude_patterns", []string{
		"*_generated.go", "*.pb.go", "*.pb.gw.go", "*.md",
		"go.sum", "package-lock.json", "yarn.lock",
	})
	v.SetDefault("static_analysis.enabled", true)
	v.SetDefault("static_analysis.workers", 4)
	v.SetDefault("static_analysis.per_file_timeout_seconds", 30)
	v.SetDefault("static_analysis.semgrep_rules_dir", "rules/semgrep")
	v.SetDefault("static_analysis.golangci_config_path", "")
	v.SetDefault("static_analysis.tools.semgrep.enabled", true)
	v.SetDefault("static_analysis.tools.golangci_lint.enabled", true)
	v.SetDefault("static_analysis.tools.gitleaks.enabled", true)
	v.SetDefault("static_analysis.adaptive.trivial_max_lines", 20)
	v.SetDefault("static_analysis.adaptive.complex_min_lines", 200)
	v.SetDefault("static_analysis.adaptive.complex_min_files", 5)

	// Environment variable bindings. BindEnv ensures that Unmarshal picks up
	// environment values even for keys that are absent from the YAML file.
	v.BindEnv("server_port", "SERVER_PORT")
	v.BindEnv("webhook_secret", "WEBHOOK_SECRET")
	v.BindEnv("github_token", "GITHUB_TOKEN")
	v.BindEnv("database_url", "DATABASE_URL")
	v.BindEnv("llm_provider", "LLM_PROVIDER")
	v.BindEnv("openai_api_key", "OPENAI_API_KEY")
	v.BindEnv("anthropic_api_key", "ANTHROPIC_API_KEY")

	if configPath != "" {
		v.SetConfigFile(configPath)
		v.SetConfigType("yaml")
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &cfg, nil
}
