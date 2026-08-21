// CLI-native forge authentication.
//
// Each forge CLI is the sole owner of its credential store; vibekit
// speaks to it only through documented subcommands. One login serves
// three consumer classes — vibekit's ForgeOps HTTP surface, git's
// credential helper for every git invoker (internal/git, the user's
// PTY shell, the agent's shell commands), and the authenticated CLI
// itself in the shell:
//
//	write:      gh auth login --with-token (stdin)
//	            glab auth login --stdin    (stdin)
//	            tea login add              (token via $GITEA_SERVER_TOKEN)
//	delete:     gh auth logout / glab auth logout / tea login delete
//	git helper: gh auth setup-git / glab auth git-credential configure /
//	            tea login add --git-credentials (rides the login)
//
// Discovery (the read verb) lives in discover.go. There is no file
// writing, no second copy of any token, and no hand-rolled config
// marshaling anywhere in this package.

package forges

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// cliLogin stores token in the kind's CLI credential store via the
// CLI's own login subcommand, and registers the CLI as git's credential
// helper for the host. The CLIs validate the token against the forge at
// login time, so a connect requires network — a bad token fails here
// instead of at first use.
func cliLogin(ctx context.Context, kind Kind, host, token string) error {
	if token == "" {
		return errors.New("forges: empty token")
	}
	if host == "" {
		host = kind.DefaultHost()
	}
	if host == "" {
		return fmt.Errorf("forges: kind %q requires a host", kind)
	}
	m, ok := kindMeta[kind]
	if !ok {
		return fmt.Errorf("forges: unhandled kind %q", kind)
	}
	if err := m.Login(ctx, host, token); err != nil {
		return fmt.Errorf("%s login: %w", m.CLI, err)
	}
	return nil
}

// cliLogout removes the kind/host credential from the CLI's store via
// the CLI's own logout subcommand. Idempotent: a host that was never
// logged in is a no-op, matching the CLIs' own "not logged in" replies.
func cliLogout(ctx context.Context, kind Kind, host string) error {
	if host == "" {
		host = kind.DefaultHost()
	}
	m, ok := kindMeta[kind]
	if !ok {
		return nil
	}
	if err := m.Logout(ctx, host); err != nil {
		if errors.Is(err, ErrNotLoggedIn) {
			return nil
		}
		return fmt.Errorf("%s logout: %w", m.CLI, err)
	}
	return nil
}

// --- gh -----------------------------------------------------------------

func loginGH(ctx context.Context, host, token string) error {
	if _, err := runCmd(ctx, CmdTimeout, []byte(token), "gh",
		"auth", "login", flagHostname, host, "--with-token"); err != nil {
		return err
	}
	// Register gh as git's credential helper for the host. Loud on
	// failure: a silently-skipped setup-git means every later git
	// push/clone against the host fails with an auth prompt.
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", "auth", "setup-git", flagHostname, host)
	return err
}

func logoutGH(ctx context.Context, host string) error {
	_, err := runCmd(ctx, CmdTimeout, nil, "gh", "auth", "logout", flagHostname, host)
	return err
}

// --- glab ---------------------------------------------------------------

func loginGLab(ctx context.Context, host, token string) error {
	if _, err := runCmd(ctx, CmdTimeout, []byte(token), "glab",
		"auth", "login", flagHostname, host, "--stdin"); err != nil {
		return err
	}
	_, err := runCmd(ctx, CmdTimeout, nil, "glab",
		"auth", "git-credential", "configure", flagHostname, host)
	return err
}

func logoutGLab(ctx context.Context, host string) error {
	_, err := runCmd(ctx, CmdTimeout, nil, "glab", "auth", "logout", flagHostname, host)
	return err
}

// --- tea ----------------------------------------------------------------

// loginTea adds a tea login named after the host. --git-credentials
// registers tea itself as git's credential helper for the login's URL
// (the same shape gh and glab use), which makes the tea binary a
// runtime dependency of HTTPS git against this host — disabling the
// tea tool breaks those pushes until it is reinstalled (surfaced by
// the cli_missing forge row, see discover.go). The token travels via
// $GITEA_SERVER_TOKEN so it never appears in the process argv.
func loginTea(ctx context.Context, host, token string) error {
	// Scrub any stale ~/.git-credentials line for this host first: git
	// consults the global "store" helper before tea's per-URL helper,
	// so a leftover cleartext entry would keep answering with a stale
	// (possibly revoked) token. Best-effort — a scrub failure must not
	// block the login.
	if err := scrubGitCredentials(ctx, host); err != nil {
		slog.Warn("forges: git-credentials scrub failed", "host", host, "error", err)
	}
	_, err := runCmdEnv(ctx, CmdTimeout, nil,
		[]string{"GITEA_SERVER_TOKEN=" + token}, cliTea,
		"login", "add", "--name", host, "--url", "https://"+host,
		"--git-credentials", "--no-version-check")
	return err
}

// logoutTea deletes the tea login for host, resolving the login NAME
// from tea's own list first: vibekit names logins after their host,
// but a login added by hand in the shell can carry any name, and the
// URL is the ground truth either way.
func logoutTea(ctx context.Context, host string) error {
	logins, err := teaLogins(ctx)
	if err != nil {
		return err
	}
	name := ""
	for _, l := range logins {
		if l.host() == host || l.Name == host {
			name = l.Name
			break
		}
	}
	if name == "" {
		return nil // nothing stored for this host — idempotent no-op
	}
	if _, err := runCmd(ctx, CmdTimeout, nil, cliTea, "login", "delete", name); err != nil {
		return err
	}
	// Retire any legacy cleartext credential for the host (written by
	// the pre-CLI-native tea integration, which disconnect never
	// cleaned up). Best-effort.
	if err := scrubGitCredentials(ctx, host); err != nil {
		slog.Warn("forges: git-credentials scrub failed", "host", host, "error", err)
	}
	return nil
}
