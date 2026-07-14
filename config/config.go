package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Bin       string   `yaml:"bin"`
	Addr      string   `yaml:"addr"`
	Data      string   `yaml:"data"`
	User      string   `yaml:"user"`
	UserAgent string   `yaml:"user_agent"` // sent to Open-Meteo when scraping
	Github    string   `yaml:"github"`     // optional repo URL, linked in the footer
	Regions   []string `yaml:"regions"`
}

type Region struct {
	Id             string `yaml:"id"`
	Name           string `yaml:"name"`
	Timezone       string `yaml:"timezone"`
	UpdateSchedule string `yaml:"update_schedule"`
	Areas          []Area `yaml:"areas"`
}

type Area struct {
	Id    string `yaml:"id"`
	Name  string `yaml:"name"`
	Drive string `yaml:"drive"`
	Crags []Crag `yaml:"crags"`
}

type Crag struct {
	Name    string  `yaml:"name"`
	Lat     float64 `yaml:"lat"`
	Lon     float64 `yaml:"lon"`
	Rock    string  `yaml:"rock"`
	Aspect  string  `yaml:"aspect"`
	Seepage bool    `yaml:"seepage"`
}

func loadConfig(p string, c any) error {
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open config %q: %w", p, err)
	}
	defer f.Close()
	return decodeConfig(f, c)
}

func decodeConfig(r io.Reader, c any) error {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	return dec.Decode(c)
}

func LoadConfig(path string) (Config, error) {
	var c Config
	if err := loadConfig(path, &c); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("config %q: %w", path, err)
	}
	return c, nil
}

func LoadRegionConfig(path string) (Region, error) {
	var c Region
	if err := loadConfig(path, &c); err != nil {
		return Region{}, err
	}
	if err := c.Validate(); err != nil {
		return Region{}, fmt.Errorf("config %q: %w", path, err)
	}
	return c, nil
}

// Validate checks the deployment config for missing fields. All fields are
// required: bin/user feed the generated cron and systemd units, addr/data feed
// serve and scrape, and user_agent identifies the deployment to Open-Meteo, so
// a config that omits any of them is incomplete for at least one command.
func (c Config) Validate() error {
	var missing []string
	for _, f := range []struct {
		name, val string
	}{
		{"bin", c.Bin},
		{"data", c.Data},
		{"addr", c.Addr},
		{"user", c.User},
		{"user_agent", c.UserAgent},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(missing, ", "))
	}
	if len(c.Regions) == 0 {
		return errors.New("at least one region is required")
	}
	return nil
}

// Validate checks a region config for missing fields, an unparseable timezone,
// and out-of-range coordinates. Errors are prefixed with the region id (and
// area/crag name) so a bad entry is easy to locate.
func (r Region) Validate() error {
	if strings.TrimSpace(r.Id) == "" {
		return errors.New("region: id is required")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("region %q: name is required", r.Id)
	}
	if strings.TrimSpace(r.Timezone) == "" {
		return fmt.Errorf("region %q: timezone is required", r.Id)
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil {
		return fmt.Errorf("region %q: invalid timezone %q: %w", r.Id, r.Timezone, err)
	}
	if strings.TrimSpace(r.UpdateSchedule) == "" {
		return fmt.Errorf("region %q: update_schedule is required", r.Id)
	}
	if len(r.Areas) == 0 {
		return fmt.Errorf("region %q: at least one area is required", r.Id)
	}
	// Area ids become filenames (data/<region>/<id>.html) and route segments, so
	// they must be present, unique within the region, and free of path/route
	// separators. Areas may have no crags — those are skipped when scraping and
	// serving, not treated as an error.
	seenIds := make(map[string]bool, len(r.Areas))
	for _, a := range r.Areas {
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("region %q: area name is required", r.Id)
		}
		if a.Id == "" {
			return fmt.Errorf("region %q: area %q: id is required", r.Id, a.Name)
		}
		if strings.ContainsAny(a.Id, "/\\ \t") {
			return fmt.Errorf("region %q: area id %q must not contain spaces or slashes", r.Id, a.Id)
		}
		if seenIds[a.Id] {
			return fmt.Errorf("region %q: duplicate area id %q", r.Id, a.Id)
		}
		seenIds[a.Id] = true
		if strings.TrimSpace(a.Drive) == "" {
			return fmt.Errorf("region %q: area %q: drive is required", r.Id, a.Name)
		}
		for _, cr := range a.Crags {
			if strings.TrimSpace(cr.Name) == "" {
				return fmt.Errorf("region %q: area %q: crag name is required", r.Id, a.Name)
			}
			if cr.Lat < -90 || cr.Lat > 90 {
				return fmt.Errorf("region %q: crag %q: lat %g out of range [-90,90]", r.Id, cr.Name, cr.Lat)
			}
			if cr.Lon < -180 || cr.Lon > 180 {
				return fmt.Errorf("region %q: crag %q: lon %g out of range [-180,180]", r.Id, cr.Name, cr.Lon)
			}
			if strings.TrimSpace(cr.Rock) == "" {
				return fmt.Errorf("region %q: crag %q: rock is required", r.Id, cr.Name)
			}
			if strings.TrimSpace(cr.Aspect) == "" {
				return fmt.Errorf("region %q: crag %q: aspect is required", r.Id, cr.Name)
			}
		}
	}
	return nil
}
