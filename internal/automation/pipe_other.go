//go:build !windows

package automation

import (
	"context"
	"net"
)

func listenLocal(string) (net.Listener, error) {
	return nil, Fail("unsupported_platform", "The automation transport requires Windows")
}
func dialLocal(context.Context, string) (net.Conn, error) {
	return nil, Fail("unsupported_platform", "The automation transport requires Windows")
}
