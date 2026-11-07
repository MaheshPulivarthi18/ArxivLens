package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    *Config // nil with wantErr false → disabled
		wantErr bool
		noEcho  string // must not appear in the error message
	}{
		{name: "model unset disables the provider", env: map[string]string{"LLM_BASE_URL": "http://x/v1", "LLM_API_KEY": "k"}},
		{name: "model only takes the local defaults", env: map[string]string{"LLM_MODEL": "llama3"},
			want: &Config{BaseURL: "http://localhost:11434/v1", Model: "llama3", Timeout: 2 * time.Second, MaxTokens: 256}},
		{name: "every variable set", env: map[string]string{"LLM_MODEL": "gpt-x", "LLM_BASE_URL": "https://api.example.com/v1/",
			"LLM_API_KEY": "sk-abc", "LLM_TIMEOUT": "500ms", "LLM_MAX_TOKENS": "8192"},
			want: &Config{BaseURL: "https://api.example.com/v1", Model: "gpt-x", Timeout: 500 * time.Millisecond, MaxTokens: 8192}},
		{name: "blank model", env: map[string]string{"LLM_MODEL": "   "}, wantErr: true},
		{name: "timeout not a duration", env: map[string]string{"LLM_MODEL": "m", "LLM_TIMEOUT": "two-seconds"}, wantErr: true, noEcho: "two-seconds"},
		{name: "timeout bare number", env: map[string]string{"LLM_MODEL": "m", "LLM_TIMEOUT": "2"}, wantErr: true},
		{name: "timeout zero", env: map[string]string{"LLM_MODEL": "m", "LLM_TIMEOUT": "0s"}, wantErr: true},
		{name: "timeout negative", env: map[string]string{"LLM_MODEL": "m", "LLM_TIMEOUT": "-1s"}, wantErr: true},
		{name: "max tokens not an int", env: map[string]string{"LLM_MODEL": "m", "LLM_MAX_TOKENS": "lots"}, wantErr: true, noEcho: "lots"},
		{name: "max tokens zero", env: map[string]string{"LLM_MODEL": "m", "LLM_MAX_TOKENS": "0"}, wantErr: true},
		{name: "max tokens negative", env: map[string]string{"LLM_MODEL": "m", "LLM_MAX_TOKENS": "-5"}, wantErr: true},
		{name: "max tokens too big", env: map[string]string{"LLM_MODEL": "m", "LLM_MAX_TOKENS": "8193"}, wantErr: true},
		{name: "base url relative", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "/v1"}, wantErr: true},
		{name: "base url no scheme", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "localhost:11434"}, wantErr: true},
		{name: "base url ftp", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "ftp://host/v1"}, wantErr: true},
		{name: "base url no host", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "http:///v1"}, wantErr: true},
		{name: "base url unparseable", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "http://h\x7f/SECRETPATH"}, wantErr: true, noEcho: "SECRETPATH"},
		{name: "base url userinfo", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "http://user:SECRETPW@h/v1"}, wantErr: true, noEcho: "SECRETPW"},
		{name: "base url query key", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "https://h/v1?key=SECRETQ"}, wantErr: true, noEcho: "SECRETQ"},
		{name: "base url empty query", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "https://h/v1?"}, wantErr: true},
		{name: "base url fragment", env: map[string]string{"LLM_MODEL": "m", "LLM_BASE_URL": "https://h/v1#frag"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ConfigFromEnv(envOf(tt.env))
			checkFromEnv(t, tt.env, tt.want, err)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got cfg=%v", cfg)
				}
				if cfg != nil {
					t.Fatalf("want nil config on error, got %v", cfg)
				}
				if tt.noEcho != "" && strings.Contains(err.Error(), tt.noEcho) {
					t.Fatalf("error echoes the value: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want == nil {
				if cfg != nil {
					t.Fatalf("want nil config, got %v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("want config, got nil")
			}
			if cfg.BaseURL != tt.want.BaseURL || cfg.Model != tt.want.Model || cfg.Timeout != tt.want.Timeout || cfg.MaxTokens != tt.want.MaxTokens {
				t.Fatalf("got %v, want %v", cfg, tt.want)
			}
			if cfg.APIKey.reveal() != tt.env["LLM_API_KEY"] {
				t.Fatalf("api key not stored")
			}
		})
	}
}

// checkFromEnv asserts FromEnv agrees with ConfigFromEnv: the same error,
// an untyped nil Provider when disabled, else a *Client with the same config.
func checkFromEnv(t *testing.T, env map[string]string, want *Config, cfgErr error) {
	t.Helper()
	p, err := FromEnv(envOf(env))
	switch {
	case cfgErr != nil:
		if err == nil || err.Error() != cfgErr.Error() || p != nil {
			t.Fatalf("FromEnv = %v, %v; want nil and the ConfigFromEnv error %q", p, err, cfgErr)
		}
	case want == nil:
		if err != nil || p != nil { // p != nil is also true for a typed nil *Client
			t.Fatalf("FromEnv = %#v, %v; want untyped nil, nil", p, err)
		}
	default:
		c, ok := p.(*Client)
		if err != nil || !ok || c == nil {
			t.Fatalf("FromEnv = %T %v, %v; want *Client", p, p, err)
		}
		if c.cfg.BaseURL != want.BaseURL || c.cfg.Model != want.Model || c.cfg.Timeout != want.Timeout || c.cfg.MaxTokens != want.MaxTokens {
			t.Fatalf("FromEnv client config %v, want %v", c.cfg, want)
		}
		if c.endpoint != want.BaseURL+"/chat/completions" || c.Name() != "openai-compatible" {
			t.Fatalf("endpoint %q name %q", c.endpoint, c.Name())
		}
	}
}

func TestConfigRedactsKey(t *testing.T) {
	const key = "sk-SENTINEL-KEY-config"
	cfg, err := ConfigFromEnv(envOf(map[string]string{"LLM_MODEL": "m", "LLM_API_KEY": key}))
	if err != nil || cfg == nil {
		t.Fatalf("ConfigFromEnv: %v %v", cfg, err)
	}
	client, err := New(*cfg)
	if err != nil {
		t.Fatal(err)
	}
	type holder struct{ cfg Config } // unexported field: fmt can't call methods on it
	type clientHolder struct{ c *Client }
	js, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outs := []string{string(js)}
	for _, v := range []any{*cfg, cfg, holder{*cfg}, &holder{*cfg}, cfg.APIKey, client, *client, clientHolder{client}, &clientHolder{client}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			outs = append(outs, fmt.Sprintf(verb, v))
		}
	}
	for _, out := range outs {
		if strings.Contains(out, key) {
			t.Fatalf("key leaked in %q", out)
		}
	}
	if !strings.Contains(client.String(), "[REDACTED]") || client.cfg.APIKey.reveal() != key {
		t.Fatalf("client String() = %q", client)
	}
	if !strings.Contains(cfg.String(), "[REDACTED]") || !strings.Contains(string(js), "[REDACTED]") {
		t.Fatalf("want [REDACTED] marker, got %s / %s", cfg, js)
	}
	if got := (Config{}).APIKey.String(); got != "[none]" {
		t.Fatalf("empty key prints %q, want [none]", got)
	}
}

func TestConfigNormalize(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		want    Config
		wantErr bool
	}{
		{name: "zero fields take defaults", cfg: Config{Model: "m"},
			want: Config{BaseURL: DefaultBaseURL, Model: "m", Timeout: DefaultTimeout, MaxTokens: DefaultMaxTokens}},
		{name: "trailing slash trimmed", cfg: Config{Model: "m", BaseURL: "https://h/v1//", Timeout: time.Second, MaxTokens: 1},
			want: Config{BaseURL: "https://h/v1", Model: "m", Timeout: time.Second, MaxTokens: 1}},
		{name: "no model", cfg: Config{}, wantErr: true},
		{name: "negative timeout", cfg: Config{Model: "m", Timeout: -time.Second}, wantErr: true},
		{name: "max tokens too big", cfg: Config{Model: "m", MaxTokens: 9000}, wantErr: true},
		{name: "negative max tokens", cfg: Config{Model: "m", MaxTokens: -1}, wantErr: true},
		{name: "bad base url", cfg: Config{Model: "m", BaseURL: "not a url"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			err := cfg.normalize()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", cfg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BaseURL != tt.want.BaseURL || cfg.Model != tt.want.Model || cfg.Timeout != tt.want.Timeout || cfg.MaxTokens != tt.want.MaxTokens {
				t.Fatalf("got %v, want %v", cfg, tt.want)
			}
		})
	}
}
