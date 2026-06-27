package gen

import (
	_ "embed"
	"fmt"
	"os"
	"text/template"
)

//go:embed systemd.tmpl
var systemdTmpl string

var systemd = template.Must(template.New("systemd").Parse(systemdTmpl))

type SystemdOptions struct {
	ConfigPath string
}

func RunSystemd(o SystemdOptions) error {
	if err := systemd.Execute(os.Stdout, o); err != nil {
		return fmt.Errorf("generate systemd: %w", err)
	}
	return nil
}
