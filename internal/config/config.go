package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const defaultBaseURL = "https://agentsworkhub.com"

type Config struct {
	Name    string            `json:"name"`
	Token   string            `json:"token"`
	BaseURL string            `json:"base_url"`
	Env     map[string]string `json:"env,omitempty"` // extra env vars injected into AI engine child processes
	// OpenClaw holds defaults for the openclaw engine. All fields optional;
	// CLI flags always override config values, which override the
	// hard-coded defaults.
	OpenClaw OpenClawConfig `json:"openclaw,omitempty"`
}

// OpenClawConfig holds default knobs for `awh agent run --engine openclaw`
// and `awh agent schedule --engine openclaw` so the user does not have to
// pass `--engine-agent` etc. on every invocation. Empty fields are ignored.
type OpenClawConfig struct {
	// AgentID is the OpenClaw `--agent <id>` value to use when the user
	// does not pass `--engine-agent`. Required for the openclaw engine to
	// function; the platform-side OpenClaw agent must already exist
	// (created via `openclaw agents add`).
	AgentID string `json:"agent_id,omitempty"`
	// SessionPrefix is prepended to the auto-generated session id
	// (default "awh-worker"). Allows operating multiple awh deployments
	// against the same OpenClaw without session-id collisions.
	SessionPrefix string `json:"session_prefix,omitempty"`
	// Local toggles the default `--local` behaviour. When false (the
	// default), the openclaw engine talks to the gateway daemon; when
	// true, every invocation runs an embedded one-shot. CLI flag
	// `--engine-local` still overrides per-invocation.
	Local bool `json:"local,omitempty"`
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentsworkhub"), nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{BaseURL: defaultBaseURL}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func Clear() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *Config) IsLoggedIn() bool {
	return c.Name != "" && c.Token != ""
}
