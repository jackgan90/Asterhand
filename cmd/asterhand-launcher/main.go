package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/launch"
)

func main() {
	loc := i18n.Effective()
	noBrowser := flag.Bool("no-browser", false, i18n.T(loc, "desktop.launcher.flag.no_browser"))
	quiet := flag.Bool("quiet", false, i18n.T(loc, "desktop.launcher.flag.quiet"))
	flag.Parse()
	executable, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(executable), "installation.json")
		launch.ApplyInstallationLocale(path)
		err = launch.Run(path, *noBrowser)
		if err != nil {
			logs := launch.LogError(path, err)
			if !*quiet {
				desktop.Error(err.Error(), logs)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Asterhand:", err)
		os.Exit(1)
	}
}
