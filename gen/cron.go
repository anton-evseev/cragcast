package gen

import (
	_ "embed"
	"fmt"
	"os"
	"text/template"

	"github.com/anton-evseev/cragcast/config"
)

//go:embed cron.tmpl
var cronTmpl string

var cron = template.Must(template.New("cron").Parse(cronTmpl))

type CronOptions struct {
	ConfigPath string
	Regions    []config.Region
}

func RunCron(o CronOptions) error {
	if err := cron.Execute(os.Stdout, o); err != nil {
		return fmt.Errorf("generate cron: %w", err)
	}
	return nil
}
