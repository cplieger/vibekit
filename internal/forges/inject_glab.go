package forges

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeGLabConfig writes glab's per-host config entry.
func writeGLabConfig(host, token, username string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "glab-cli")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir glab-cli: %w", err)
	}
	path := filepath.Join(dir, "config.yml")
	cfg, err := loadGLabConfig(path)
	if err != nil {
		return err
	}
	if cfg.Hosts == nil {
		cfg.Hosts = make(map[string]glabHostEntry)
	}
	cfg.Hosts[host] = glabHostEntry{
		Token:    token,
		User:     username,
		Protocol: protoHTTPS,
		APIHost:  host,
	}
	// Editor is left as loaded from the existing config; glab falls back
	// to $EDITOR when it is blank, so there is nothing to set here.
	return writeYAML(path, marshalGLabConfig(cfg))
}

func removeGLabHost(host string) error {
	root, err := configHome()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "glab-cli", "config.yml")
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}
	cfg, err := loadGLabConfig(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	delete(cfg.Hosts, host)
	if len(cfg.Hosts) == 0 {
		return os.Remove(path)
	}
	return writeYAML(path, marshalGLabConfig(cfg))
}

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
	// Top-level key.
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
	// 4-space indent → host name; 8-space indent → host fields.
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

func marshalGLabConfig(cfg *glabConfig) string {
	var b strings.Builder
	if cfg.Editor != "" {
		fmt.Fprintf(&b, "editor: %s\n", cfg.Editor)
	}
	if len(cfg.Hosts) > 0 {
		b.WriteString("hosts:\n")
		for host, e := range cfg.Hosts {
			writeGLabHost(&b, host, e)
		}
	}
	return b.String()
}

// writeGLabHost renders one host subtree into b, matching glab's writer.
func writeGLabHost(b *strings.Builder, host string, e glabHostEntry) {
	fmt.Fprintf(b, "    %s:\n", host)
	if e.Token != "" {
		fmt.Fprintf(b, "        token: %s\n", e.Token)
	}
	if e.User != "" {
		fmt.Fprintf(b, "        user: %s\n", e.User)
	}
	gp := e.Protocol
	if gp == "" {
		gp = protoHTTPS
	}
	fmt.Fprintf(b, "        git_protocol: %s\n", gp)
	if e.APIHost != "" {
		fmt.Fprintf(b, "        api_host: %s\n", e.APIHost)
	}
}

func setupGitGLab(ctx context.Context, host string) error {
	args := []string{"auth", "git-credential", "configure", flagHostname, host}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil && errors.Is(err, ErrNotInstalled) {
		return err
	}
	return nil
}
