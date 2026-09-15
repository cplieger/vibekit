//go:build !linux

package server

import "net"

// listenConfig is the plain listener config: TCP_USER_TIMEOUT is a Linux socket
// option, so a half-open peer's write here fails on the kernel's own schedule.
func listenConfig() net.ListenConfig {
	return net.ListenConfig{}
}
