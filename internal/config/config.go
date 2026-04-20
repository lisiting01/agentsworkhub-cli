package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// patrolConfigWire is used only during JSON unmarshal to handle the renamed
// auto_accept → auto_bid field while keeping backward compatibility with old
// config files that still use auto_accept.
type patrolConfigWire struct {
	Engine           string   `json:"engine"`
	EnginePath       string   `json:"engine_path"`
	EngineModel      string   `json:"engine_model"`
	EngineArgs       []string `json:"engine_args"`
	AutoBid          *bool    `json:"auto_bid"`
	AutoAcceptLegacy *bool    `json:"auto_accept"` // deprecated key, migrated on load
	BidMessage       string   `json:"bid_message"`
	MaxConcurrent    int      `json:"max_concurrent"`
	PollIntervalSecs int      `json:"poll_interval_secs"`
	SkillsFilter     []string `json:"skills_filter"`
	WorkDir          string   `json:"work_dir"`
	TaskTimeoutMins  int      `json:"task_timeout_mins"`

	PublisherAutoSelectBid  bool   `json:"publisher_auto_select_bid"`
	PublisherAutoComplete   bool   `json:"publisher_auto_complete"`
	PublisherSelectStrategy string `json:"publisher_select_strategy"`
}

const defaultBaseURL = "https://agentsworkhub.com"

type PatrolConfig struct {
	Engine           string   `json:"engine"`
	EnginePath       string   `json:"engine_path"`
	EngineModel      string   `json:"engine_model"`
	EngineArgs       []string `json:"engine_args"`
	AutoBid          bool     `json:"auto_bid"`
	BidMessage       string   `json:"bid_message"`
	MaxConcurrent    int      `json:"max_concurrent"`
	PollIntervalSecs int      `json:"poll_interval_secs"`
	SkillsFilter     []string `json:"skills_filter"`
	WorkDir          string   `json:"work_dir"`
	TaskTimeoutMins  int      `json:"task_timeout_mins"`

	// Publisher role settings
	PublisherAutoSelectBid  bool   `json:"publisher_auto_select_bid"`
	PublisherAutoComplete   bool   `json:"publisher_auto_complete"`
	PublisherSelectStrategy string `json:"publisher_select_strategy"` // "first" (default)
}

type Config struct {
	Name    string            `json:"name"`
	Token   string            `json:"token"`
	BaseURL string            `json:"base_url"`
	Patrol  PatrolConfig      `json:"patrol"`
	Env     map[string]string `json:"env,omitempty"` // extra env vars injected into AI engine child processes
}

func defaultPatrolConfig() PatrolConfig {
	return PatrolConfig{
		Engine:           "claude",
		EnginePath:       "claude",
		EngineArgs:       []string{},
		AutoBid:          true,
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

// configWire mirrors Config but uses patrolConfigWire for backward-compat migration.
type configWire struct {
	Name    string            `json:"name"`
	Token   string            `json:"token"`
	BaseURL string            `json:"base_url"`
	Patrol  patrolConfigWire  `json:"patrol"`
	Env     map[string]string `json:"env,omitempty"`
}

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := &Config{BaseURL: defaultBaseURL}
		applyPatrolDefaults(&cfg.Patrol)
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	var wire configWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}

	cfg := &Config{
		Name:    wire.Name,
		Token:   wire.Token,
		BaseURL: wire.BaseURL,
		Env:     wire.Env,
		Patrol: PatrolConfig{
			Engine:                  wire.Patrol.Engine,
			EnginePath:              wire.Patrol.EnginePath,
			EngineModel:             wire.Patrol.EngineModel,
			EngineArgs:              wire.Patrol.EngineArgs,
			BidMessage:              wire.Patrol.BidMessage,
			MaxConcurrent:           wire.Patrol.MaxConcurrent,
			PollIntervalSecs:        wire.Patrol.PollIntervalSecs,
			SkillsFilter:            wire.Patrol.SkillsFilter,
			WorkDir:                 wire.Patrol.WorkDir,
			TaskTimeoutMins:         wire.Patrol.TaskTimeoutMins,
			PublisherAutoSelectBid:  wire.Patrol.PublisherAutoSelectBid,
			PublisherAutoComplete:   wire.Patrol.PublisherAutoComplete,
			PublisherSelectStrategy: wire.Patrol.PublisherSelectStrategy,
		},
	}

	// Migrate: prefer new auto_bid key, fall back to legacy auto_accept
	switch {
	case wire.Patrol.AutoBid != nil:
		cfg.Patrol.AutoBid = *wire.Patrol.AutoBid
	case wire.Patrol.AutoAcceptLegacy != nil:
		cfg.Patrol.AutoBid = *wire.Patrol.AutoAcceptLegacy
	default:
		cfg.Patrol.AutoBid = true // default true when key absent entirely
	}

	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	applyPatrolDefaults(&cfg.Patrol)
	return cfg, nil
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
	if d.PublisherSelectStrategy == "" {
		d.PublisherSelectStrategy = "first"
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
