module vibekit

go 1.26.4

require github.com/creack/pty v1.1.24

require (
	github.com/coder/websocket v1.8.14
	github.com/cplieger/atomicfile v0.0.0
	golang.org/x/sync v0.20.0
	pgregory.net/rapid v1.3.0
)

replace github.com/cplieger/atomicfile => /workspace/atomicfile
