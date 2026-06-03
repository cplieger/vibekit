module vibekit

go 1.26.4

require github.com/creack/pty v1.1.24

require (
	github.com/cplieger/vterm v0.1.0
	golang.org/x/sync v0.20.0
	pgregory.net/rapid v1.3.0
)

require github.com/coder/websocket v1.8.14 // indirect

replace github.com/cplieger/vterm => /workspace/vterm
