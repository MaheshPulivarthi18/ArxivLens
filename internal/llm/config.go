package llm

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults for the environment config.
//
// DefaultTimeout and DefaultMaxTokens are a budget, not a measurement. The
// /v1/ask p95 target is 3 s (Product PRD §8); search, rerank and citation
// validation are budgeted at ≤ 1 s, which leaves 2 s for the whole LLM call
// (both attempts and the backoff). 256 output tokens fits a 4–6 sentence cited
// answer from a hosted small-tier model in that time. They are a starting
// point: EVAL-8 measures the real p95. A local 7–8B model on a laptop decodes
// at roughly 20–30 tok/s and will usually time out at 2 s and fall back to the
// template answer; raise LLM_TIMEOUT if you accept the slower answer.
const (
	DefaultBaseURL   = "http://localhost:11434/v1" // Ollama's OpenAI-compatible API
	DefaultTimeout   = 2 * time.Second
	DefaultMaxTokens = 256
	maxMaxTokens     = 8192
)

// Secret holds the API key. Every print form is "[REDACTED]". It stores a
// pointer so that even fmt's raw dump (a Config in an unexported field, where
// fmt can't call methods) shows an address, not the key.
type Secret struct{ v *string }

// NewSecret wraps a key. An empty key means "send no Authorization header".
func NewSecret(s string) Secret {
	if s == "" {
		return Secret{}
	}
	return Secret{v: &s}
}

func (s Secret) reveal() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

func (s Secret) String() string {
	if s.v == nil {
		return "[none]"
	}
	return "[REDACTED]"
}

func (s Secret) GoString() string             { return s.String() }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(s.String())), nil }

// Config configures the OpenAI-compatible provider. normalize fills zero BaseURL, Timeout and MaxTokens with the defaults.
type Config struct {
	BaseURL   string // OpenAI-compatible root, e.g. http://localhost:11434/v1
	Model     string // required
	APIKey    Secret
	Timeout   time.Duration // the whole Complete call, retries included
	MaxTokens int           // used when Request.MaxTokens is 0
}

func (c Config) String() string {
	return fmt.Sprintf("llm.Config{base_url: %s, model: %s, api_key: %s, timeout: %s, max_tokens: %d}",
		c.BaseURL, c.Model, c.APIKey, c.Timeout, c.MaxTokens)
}

func (c Config) GoString() string { return c.String() }

// ConfigFromEnv reads LLM_MODEL, LLM_BASE_URL, LLM_API_KEY, LLM_TIMEOUT and
// LLM_MAX_TOKENS through getenv (os.Getenv in production). It returns nil, nil
// when LLM_MODEL is unset: no provider, so answers stay templated. Invalid
// values are an error, never a silent default. Error messages name the
// variable but never echo its value.
func ConfigFromEnv(getenv func(string) string) (*Config, error) {
	model := getenv("LLM_MODEL")
	if model == "" {
		return nil, nil
	}
	cfg := Config{
		BaseURL:   getenv("LLM_BASE_URL"),
		Model:     model,
		APIKey:    NewSecret(getenv("LLM_API_KEY")),
		Timeout:   DefaultTimeout,
		MaxTokens: DefaultMaxTokens,
	}
	if s := getenv("LLM_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return nil, errors.New("llm: LLM_TIMEOUT must be a positive Go duration, e.g. 2s")
		}
		cfg.Timeout = d
	}
	if s := getenv("LLM_MAX_TOKENS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxMaxTokens {
			return nil, fmt.Errorf("llm: LLM_MAX_TOKENS must be an integer in 1..%d", maxMaxTokens)
		}
		cfg.MaxTokens = n
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// FromEnv builds the provider from the environment (see ConfigFromEnv). It
// returns an untyped nil Provider and no error when LLM_MODEL is unset, so a
// caller's `if p == nil` keeps the template answer.
func FromEnv(getenv func(string) string) (Provider, error) {
	cfg, err := ConfigFromEnv(getenv)
	if err != nil || cfg == nil {
		return nil, err
	}
	c, err := New(*cfg)
	if err != nil { // unreachable today (ConfigFromEnv already normalised cfg); kept so a new check in New is never dropped
		return nil, err
	}
	return c, nil
}

// normalize applies defaults to zero fields and validates the rest.
func (c *Config) normalize() error {
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("llm: LLM_MODEL must not be blank")
	}
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	if c.Timeout < 0 {
		return errors.New("llm: LLM_TIMEOUT must be positive")
	}
	if c.MaxTokens < 1 || c.MaxTokens > maxMaxTokens {
		return fmt.Errorf("llm: LLM_MAX_TOKENS must be in 1..%d", maxMaxTokens)
	}
	// The parse error is dropped on purpose: url errors quote the input.
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("llm: LLM_BASE_URL must be an absolute http(s) URL")
	}
	// A key in userinfo or the query (?key=) would end up in net/http's *url.Error.
	if u.User != nil || strings.ContainsAny(c.BaseURL, "?#") {
		return errors.New("llm: LLM_BASE_URL must not contain userinfo, a query or a fragment; put the key in LLM_API_KEY")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return nil
}
