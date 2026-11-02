//go:build windows

package quickinput

import (
	"github.com/jackgan90/Asterhand/internal/platform/userprocess"
	"golang.org/x/sys/windows"
)

func restrictedHostToken() (windows.Token, error) { return userprocess.OrdinaryToken() }
func verifyHostToken(token windows.Token) error   { return userprocess.VerifyToken(token) }
