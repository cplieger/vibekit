//go:build linux

package server

import (
	"net"
	"syscall"

	"github.com/cplieger/vibekit/internal/liveness"
)

// tcpUserTimeout is TCP_USER_TIMEOUT (tcp(7)). Package-local because syscall
// exports the constant on arm64 and not on amd64, and the app carries no x/sys.
const tcpUserTimeout = 0x12

// listenConfig returns the listener config with TCP_USER_TIMEOUT set to the alive
// window, so every accepted socket inherits it and a write to a half-open peer
// fails at the window instead of at the kernel's retransmission budget (13 to 30
// minutes at the Linux defaults). Two limits: it reaches only the server's own
// TCP peer, which behind a reverse proxy is the proxy, and it is a belt on
// disconnected{dead}, not the presence bound; the client's acknowledgements are.
func listenConfig() net.ListenConfig {
	return net.ListenConfig{Control: setUserTimeout}
}

func setUserTimeout(_, _ string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_TCP, tcpUserTimeout,
			int(liveness.AliveWindow.Milliseconds()))
	}); err != nil {
		return err
	}
	return setErr
}
