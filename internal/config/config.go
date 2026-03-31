package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const defaultBaseURL = "https://agentsworkhub.com"

type PatrolConfig struct {
	Engine           string   `json:"engine"`
	EnginePath       string   `json:"engine_path"`
	EngineArgs       []string `json:"engine_args"`
	AutoAccept       bool     `json:"auto_accept"`
	BidMessage       string   `json:"bid_message"`
	MaxConcurrent    int      `json:"max_concurrent"`
	PollIntervalSecs int      `json:"poll_interval_secs"`
	SkillsFilter     []string `json:"skills_filter"`
	WorkDir          string   `json:"work_dir"`
	TaskTimeoutMins  int      `json:"task_timeout_mins"`
}

type Config struct {
	Name    string       `json:"name"`
	Token   string       `json:"token"`
	BaseURL string       `json:"base_url"`
	Patrol  PatrolConfig `json:"patrol"`
}

func defaultPatrolConfig() PatrolConfig {
	return PatrolConfig{
		Engine:           "claude",
		EnginePath:       "claude",
		EngineArgs:       []string{},
		AutoAccept:       true,
		BidMessage:       "I am an automated agent ready to work on this task.",
		MaxConcurrent:    1,
		PollIntervalSecs: 30,
		SkillsFilter:     []string{},
		WorkDir:          "",
		TaskTimeoutMins:  60,
	}
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
	applyPatrolDefaults(&cfg.Patrol)
	return &cfg, nil
}

func applyPatrolDefaults(d *PatrolConfig) {
	def := defaultPatrolConfig()
	if d.Engine == "" {
		d.Engine = def.Engine
	}
	if d.EnginePath == "" {
		d.EnginePath = def.EnginePath
	}
	if d.EngineArgs == nil {
		d.EngineArgs = def.EngineArgs
	}
	if d.BidMessage == "" {
		d.BidMessage = def.BidMessage
	}
	if d.MaxConcurrent == 0 {
		d.MaxConcurrent = def.MaxConcurrent
	}
	if d.PollIntervalSecs == 0 {
		d.PollIntervalSecs = def.PollIntervalSecs
	}
	if d.SkillsFilter == nil {
		d.SkillsFilter = def.SkillsFilter
	}
	if d.TaskTimeoutMins == 0 {
		d.TaskTimeoutMins = def.TaskTimeoutMins
	}
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
