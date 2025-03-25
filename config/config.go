// config/config.go
package config

import (
	"fmt"
	"io/ioutil"
	"log"

	"truss/bluesky"
	"truss/mastodon"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Mastodon      mastodon.ClientConfig `toml:"mastodon"`
	Bluesky       bluesky.ClientConfig  `toml:"bluesky"`
	PollInterval  int                   `toml:"poll_interval"` // in seconds
	DatabasePath  string                `toml:"database_path"`
	FilterHashtag string                `toml:"filter_hashtag"`
}

// Load loads configuration from a TOML file
func Load(path string) (*Config, error) {
	log.Printf("Loading config from: %s", path)

	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Set defaults
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60 // Default to 60 seconds
	}

	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "truss.db"
	}

	// Validate required fields
	if cfg.Mastodon.Server == "" {
		return nil, fmt.Errorf("mastodon server is required in config")
	}

	if cfg.Mastodon.AccessToken == "" {
		return nil, fmt.Errorf("mastodon access token is required in config")
	}

	return &cfg, nil
}
