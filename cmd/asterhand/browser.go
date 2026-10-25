package main

import "github.com/jackgan90/Asterhand/internal/desktop"

func openBrowser(address string) error { return desktop.Open(address) }
