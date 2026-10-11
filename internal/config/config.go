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
