// Package config loads server + rule configuration from YAML.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Aashan47/Distributed-Rate-Limiter/internal/limiter"
)

type Config struct {
	Server ServerConfig `yaml:"server"`
	Redis  RedisConfig  `yaml:"redis"`
	Rules  []RuleConfig `yaml:"rules"`
}

type ServerConfig struct {
	GRPCAddr string `yaml:"grpc_addr"`
	HTTPAddr string `yaml:"http_addr"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type RuleConfig struct {
	Name      string        `yaml:"name"`
	Limit     uint32        `yaml:"limit"`
	Window    time.Duration `yaml:"window"`
	Algorithm string        `yaml:"algorithm"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.GRPCAddr == "" {
		c.Server.GRPCAddr = ":9090"
	}
	if c.Server.HTTPAddr == "" {
		c.Server.HTTPAddr = ":8080"
	}
	if c.Redis.Addr == "" {
		c.Redis.Addr = "localhost:6379"
	}
	for i := range c.Rules {
		if c.Rules[i].Algorithm == "" {
			c.Rules[i].Algorithm = "sliding_window_log"
		}
	}
}

func (c *Config) validate() error {
	if len(c.Rules) == 0 {
		return errors.New("config: at least one rule required")
	}
	seen := make(map[string]struct{}, len(c.Rules))
	for _, r := range c.Rules {
		if r.Name == "" {
			return errors.New("config: rule name cannot be empty")
		}
		if _, dup := seen[r.Name]; dup {
			return fmt.Errorf("config: duplicate rule name %q", r.Name)
		}
		seen[r.Name] = struct{}{}
		if r.Limit == 0 {
			return fmt.Errorf("config: rule %q limit must be > 0", r.Name)
		}
		if r.Window <= 0 {
			return fmt.Errorf("config: rule %q window must be > 0", r.Name)
		}
	}
	return nil
}

func (c *Config) ToLimiterRules() []limiter.Rule {
	out := make([]limiter.Rule, len(c.Rules))
	for i, r := range c.Rules {
		out[i] = limiter.Rule{
			Name:      r.Name,
			Limit:     r.Limit,
			Window:    r.Window,
			Algorithm: r.Algorithm,
		}
	}
	return out
}
