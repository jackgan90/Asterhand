package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/desktop"
	"github.com/jackgan90/Asterhand/internal/launch"
)

func main() {
	noBrowser := flag.Bool("no-browser", false, "启动节点但不打开浏览器")
	quiet := flag.Bool("quiet", false, "失败只返回非零状态，不弹出对话框")
	flag.Parse()
	executable, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(executable), "installation.json")
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
