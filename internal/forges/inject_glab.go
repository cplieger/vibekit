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
	if cfg.Editor == "" {
		cfg.Editor = "" // leave blank; glab uses $EDITOR
	}
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

func loadGLabConfig(path string) (*glabConfig, error) {
	cfg := &glabConfig{Hosts: make(map[string]glabHostEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return nil, err
	}
	// glab config.yml has a flat top-level + a hosts: subtree.
	var inHosts bool
	var currentHost string
	for line := range strings.SplitSeq(string(data), "\n") {
		stripped := strings.TrimRight(line, " \t")
		if stripped == "" {
			continue
		}
		// Top-level key.
		if !strings.HasPrefix(line, " ") {
			key := strings.TrimSuffix(strings.TrimSpace(line), ":")
			if key == "hosts" {
				inHosts = true
				continue
			}
			inHosts = false
			currentHost = ""
			continue
		}
		if !inHosts {
			continue
		}
		// 4-space indent → host name; 8-space indent → host fields.
		switch {
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "        "):
			name := strings.TrimSuffix(strings.TrimSpace(line), ":")
			currentHost = name
			cfg.Hosts[currentHost] = glabHostEntry{}
		case strings.HasPrefix(line, "        ") && currentHost != "":
			trimmed := strings.TrimSpace(line)
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			entry := cfg.Hosts[currentHost]
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
			cfg.Hosts[currentHost] = entry
		}
	}
	return cfg, nil
}

func marshalGLabConfig(cfg *glabConfig) string {
	var b strings.Builder
	if cfg.Editor != "" {
		fmt.Fprintf(&b, "editor: %s\n", cfg.Editor)
	}
	if len(cfg.Hosts) > 0 {
		b.WriteString("hosts:\n")
		for host, e := range cfg.Hosts {
			fmt.Fprintf(&b, "    %s:\n", host)
			if e.Token != "" {
				fmt.Fprintf(&b, "        token: %s\n", e.Token)
			}
			if e.User != "" {
				fmt.Fprintf(&b, "        user: %s\n", e.User)
			}
			gp := e.Protocol
			if gp == "" {
				gp = protoHTTPS
			}
			fmt.Fprintf(&b, "        git_protocol: %s\n", gp)
			if e.APIHost != "" {
				fmt.Fprintf(&b, "        api_host: %s\n", e.APIHost)
			}
		}
	}
	return b.String()
}

func setupGitGLab(ctx context.Context, host string) error {
	args := []string{"auth", "git-credential", "configure", flagHostname, host}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", args...)
	if err != nil && errors.Is(err, ErrNotInstalled) {
		return err
	}
	return nil
}
