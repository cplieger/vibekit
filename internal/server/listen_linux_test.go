//go:build linux

package server

import (
	"net"
	"syscall"
	"testing"

	"github.com/cplieger/vibekit/internal/liveness"
)

// The option is set on the LISTENER and inherited by accept(2): the accepted side
// is what serves the stream, so that is the socket the value is read back from.
func TestListenConfig_AcceptedSocketInheritsTheUserTimeout(t *testing.T) {
	lc := listenConfig()
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	peer, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer peer.Close()
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer accepted.Close()

	raw, err := accepted.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatalf("syscall conn: %v", err)
	}
	var got int
	var getErr error
	if err := raw.Control(func(fd uintptr) {
		got, getErr = syscall.GetsockoptInt(int(fd), syscall.SOL_TCP, tcpUserTimeout)
	}); err != nil {
		t.Fatalf("control: %v", err)
	}
	if getErr != nil {
		t.Fatalf("getsockopt TCP_USER_TIMEOUT: %v", getErr)
	}
	if want := int(liveness.AliveWindow.Milliseconds()); got != want {
		t.Errorf("TCP_USER_TIMEOUT on the accepted socket = %d ms, want %d ms (liveness.AliveWindow)", got, want)
	}
}
