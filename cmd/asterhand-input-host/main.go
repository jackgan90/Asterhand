package main

import (
	"github.com/jackgan90/Asterhand/internal/quickinput"
	"os"
)

func main() {
	if quickinput.RunInputHost(os.Args[1:]) != nil {
		os.Exit(1)
	}
}
