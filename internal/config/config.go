package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LLM      LLMConfig      `yaml:"llm"`
	Agent    AgentConfig    `yaml:"agent"`
	Watchers WatcherConfig  `yaml:"watchers"`
	MCP      MCPConfig      `yaml:"mcp"`
}

type LLMConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url"`
}

type AgentConfig struct {
	Name     string `yaml:"name"`
	LogLevel string `yaml:"log_level"`
	StateDir string `yaml:"state_dir"`
}

type WatcherConfig struct {
	Git        bool            `yaml:"git"`
	Filesystem bool            `yaml:"filesystem"`
	Webhook    WebhookConfig   `yaml:"webhook"`
	Cron       CronConfig      `yaml:"cron"`
}

type WebhookConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
	Secret  string `yaml:"secret"`
}

type CronConfig struct {
	Enabled bool `yaml:"enabled"`
}

type MCPConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

func DefaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-5",
		},
		Agent: AgentConfig{
			Name:     "agentd",
			LogLevel: "info",
			StateDir: "~/.agentd",
		},
		Watchers: WatcherConfig{
			Git:        true,
			Filesystem: true,
			Webhook: WebhookConfig{
				Enabled: true,
				Port:    7777,
			},
			Cron: CronConfig{
				Enabled: true,
			},
		},
		MCP: MCPConfig{
			Enabled: true,
			Port:    7778,
		},
	}
}

func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	cfg.LLM.APIKey = expandEnv(cfg.LLM.APIKey)
	cfg.Agent.StateDir = expandPath(cfg.Agent.StateDir)

	return cfg, nil
}

func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	return os.WriteFile(path, data, 0o644)
}

func (c Config) StateDir() string {
	return expandPath(c.Agent.StateDir)
}

func (c Config) SkillsDir() string {
	return filepath.Join(c.StateDir(), "skills")
}

func (c Config) DBPath() string {
	return filepath.Join(c.StateDir(), "agentd.db")
}

func (c Config) LogPath() string {
	return filepath.Join(c.StateDir(), "agentd.log")
}

func (c Config) PIDPath() string {
	return filepath.Join(c.StateDir(), "agentd.pid")
}

func (c Config) ConfigPath() string {
	return filepath.Join(c.StateDir(), "config.yaml")
}

func expandEnv(s string) string {
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		key := s[2 : len(s)-1]
		return os.Getenv(key)
	}
	return os.ExpandEnv(s)
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func EnsureDirs(cfg Config) error {
	dirs := []string{
		cfg.StateDir(),
		cfg.SkillsDir(),
		filepath.Join(cfg.StateDir(), "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}
