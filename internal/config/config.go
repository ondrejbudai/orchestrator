package config

import (
	"fmt"
	"os"
	"path"

	"github.com/goccy/go-yaml"
)

// DaemonConfig holds settings for the daemon polling loop.
type DaemonConfig struct {
	PollInterval      string   `yaml:"poll_interval"`
	AllowedCommenters []string `yaml:"allowed_commenters"`
}

type Config struct {
	SlackWebhook string       `yaml:"slack_webhook"`
	OutputDir    string       `yaml:"output_dir"`
	GjollEnv     string       `yaml:"gjoll_env"`
	AllowedRepos []string     `yaml:"allowed_repos"`
	Daemon       DaemonConfig `yaml:"daemon"`
}

// RepoAllowed reports whether repo is permitted by the AllowedRepos allowlist.
// Each entry may contain wildcards understood by path.Match (e.g. "org/*").
// An empty list denies all repos.
func (c *Config) RepoAllowed(repo string) bool {
	for _, pattern := range c.AllowedRepos {
		if matched, _ := path.Match(pattern, repo); matched {
			return true
		}
	}
	return false
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.OutputDir == "" {
		cfg.OutputDir = "./tasks"
	}
	if cfg.GjollEnv == "" {
		cfg.GjollEnv = "./configs/sandbox.tf"
	}

	return cfg, nil
}
