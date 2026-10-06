package gen

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/anton-evseev/cragcast/config"
)

//go:embed cron.tmpl
var cronTmpl string

//go:embed systemd.tmpl
var systemdTmpl string

var (
	cron    = template.Must(template.New("cron").Funcs(template.FuncMap{"quote": shellQuote}).Parse(cronTmpl))
	systemd = template.Must(template.New("systemd").Parse(systemdTmpl))
)

type CronOptions struct {
	Bin        string
	User       string
	Regions    []config.Region
	RegionsDir string
	Out        string
	UserAgent  string
	Github     string
}

type SystemdOptions struct {
	ConfigPath string
	Bin        string
	User       string
}

func RunCron(o CronOptions) error {
	if err := cron.Execute(os.Stdout, o); err != nil {
		return fmt.Errorf("generate cron: %w", err)
	}
	return nil
}

func RunSystemd(o SystemdOptions) error {
	if err := systemd.Execute(os.Stdout, o); err != nil {
		return fmt.Errorf("generate systemd: %w", err)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
