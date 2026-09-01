// Read-only glab config discovery parser — the ONE documented exception
// to this package's "never touch another program's config file" rule
// (see the provider.go package comment). glab ships no machine-readable
// auth-status output (`glab auth status` is human text only), so
// discovering which hosts glab is logged into means reading its
// config.yml. Read-only: vibekit never writes this file — token writes,
// git-credential setup, and disconnect all go through glab's own
// subcommands (auth.go). Retire this parser the day glab ships a JSON
// status output.

package forges

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

type glabHostEntry struct {
	Token    string
	User     string
	Protocol string
	APIHost  string
}

type glabConfig struct {
	Hosts  map[string]glabHostEntry
	Editor string
}

// glabParser accumulates a glabConfig while scanning config.yml line by line.
type glabParser struct {
	cfg         *glabConfig
	currentHost string
	inHosts     bool
}

// parseLine folds a single config.yml line into the accumulating config.
func (p *glabParser) parseLine(line string) {
	if strings.TrimRight(line, " \t") == "" {
		return
	}
	if !strings.HasPrefix(line, " ") {
		key := strings.TrimSuffix(strings.TrimSpace(line), ":")
		if key == "hosts" {
			p.inHosts = true
			return
		}
		p.inHosts = false
		p.currentHost = ""
		return
	}
	if !p.inHosts {
		return
	}
	switch {
	case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "        "):
		p.beginHost(line)
	case strings.HasPrefix(line, "        ") && p.currentHost != "":
		p.setHostField(line)
	}
}

// beginHost starts a new host subtree (or clears the current one for a
// blank name).
func (p *glabParser) beginHost(line string) {
	name := strings.TrimSuffix(strings.TrimSpace(line), ":")
	if name == "" {
		p.currentHost = ""
		return
	}
	p.currentHost = name
	p.cfg.Hosts[name] = glabHostEntry{}
}

// setHostField assigns a parsed key/value onto the current host entry.
func (p *glabParser) setHostField(line string) {
	key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	entry := p.cfg.Hosts[p.currentHost]
	switch key {
	case "token":
		entry.Token = val
	case fieldUser:
		entry.User = val
	case "git_protocol":
		entry.Protocol = val
	case "api_host":
		entry.APIHost = val
	}
	p.cfg.Hosts[p.currentHost] = entry
}

func loadGLabConfig(path string) (*glabConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &glabConfig{Hosts: make(map[string]glabHostEntry)}, nil
		}
		return nil, err
	}
	// glab config.yml has a flat top-level + a hosts: subtree.
	p := &glabParser{cfg: &glabConfig{Hosts: make(map[string]glabHostEntry)}}
	for line := range strings.SplitSeq(string(data), "\n") {
		p.parseLine(line)
	}
	return p.cfg, nil
}
