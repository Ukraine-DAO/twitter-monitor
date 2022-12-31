package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Channels []Channel `yaml:"channels"`
}

type Channel struct {
	Name      string
	DiscordID string `yaml:"discord_id"`

	RemoteJSON string `yaml:"remote_json"`
	Users      []TwitterUser
}

type TwitterUser struct {
	ID   string `yaml:"id"`
	Name string
}

type accountList struct {
	Entries []struct {
		ID string `json:"id"`
	} `json:"entries"`
}

func FromFile(configPath string) (*Config, error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return cfg, nil
}

func (c *Config) TwitterIDToChannels() (map[string][]string, error) {
	r := map[string][]string{}

	for _, ch := range c.Channels {
		if ch.RemoteJSON != "" {
			resp, err := http.Get(ch.RemoteJSON)
			if err != nil {
				return nil, fmt.Errorf("fetching account list %q: %w", ch.RemoteJSON, err)
			}

			list := &accountList{}
			if err := json.NewDecoder(resp.Body).Decode(list); err != nil {
				return nil, fmt.Errorf("decoding the account list %q: %w", ch.RemoteJSON, err)
			}

			for _, e := range list.Entries {
				if e.ID != "" {
					r[e.ID] = append(r[e.ID], ch.DiscordID)
				}
			}

		}
		for _, u := range ch.Users {
			r[u.ID] = append(r[u.ID], ch.DiscordID)
		}
	}

	return r, nil

}
