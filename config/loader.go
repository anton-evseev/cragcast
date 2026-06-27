package config

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Regions []Region `yaml:"regions"`
}

type Region struct {
	Id             string `yaml:"id"`
	Name           string `yaml:"name"`
	Timezone       string `yaml:"timezone"`
	UpdateSchedule string `yaml:"update_schedule"`
}

func decodeConfig(r io.Reader) (Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return c, nil
}

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer f.Close()
	return decodeConfig(f)
}
