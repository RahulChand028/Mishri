package config

import (
	"encoding/json"
	"log"
	"os"
)

type Config struct {
	App       AppConfig                 `json:"app"`
	Gateways  map[string]GatewayConfig  `json:"gateways"`
	Providers map[string]ProviderConfig `json:"providers"`
	Memory    MemoryConfig              `json:"memory"`
}

type AppConfig struct {
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
}

type GatewayConfig struct {
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
}

type ProviderConfig struct {
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url,omitempty"`
	Enabled bool   `json:"enabled"`
}

type MemoryConfig struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

func LoadConfig(path string) *Config {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf("failed to open config file: %v", err)
	}
	defer file.Close()

	var cfg Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		log.Fatalf("failed to decode config file: %v", err)
	}

	return &cfg
}

// GetDefaultProvider returns the first enabled provider
func (c *Config) GetDefaultProvider() (string, ProviderConfig) {
	for name, p := range c.Providers {
		if p.Enabled {
			return name, p
		}
	}
	return "", ProviderConfig{}
}

// GetTelegramConfig returns telegram config if enabled
func (c *Config) GetTelegramConfig() (GatewayConfig, bool) {
	tg, ok := c.Gateways["telegram"]
	if ok && tg.Enabled {
		return tg, true
	}
	return GatewayConfig{}, false
}
