// Legacy ~/.git-credentials scrubbing.
//
// The pre-CLI-native tea integration wrote the forge token in cleartext
// into ~/.git-credentials (git's "store" helper format) and never
// removed it on disconnect. Under the CLI-native model tea itself is
// the credential helper, so those lines are not just leaked material —
// git consults the global store helper FIRST, so a stale line would
// shadow tea's live answer. Login and logout for a tea host both scrub
// the host's lines.

package forges

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cplieger/atomicfile/v3"
)

// scrubGitCredentials removes every ~/.git-credentials line that
// carries a credential for host. Removing the file's last line removes
// the file. A missing file is a no-op.
//
// ctx bounds the rewrite. It used to be context.Background(), which meant a
// login or logout cancelled mid-flight still wrote the file, and the write is
// the one step here that touches the disk — both callers already hold a ctx
// with the CLI command's own timeout on it, so the substitution was discarding
// a signal that was in scope at every call site.
func scrubGitCredentials(ctx context.Context, host string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	credFile := filepath.Join(home, ".git-credentials")
	data, err := os.ReadFile(credFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var kept []string
	changed := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}
		// Store-format lines look like https://user:token@host or
		// https://user:token@host/; match on the authority suffix.
		if strings.Contains(line, "@"+host+"/") || strings.HasSuffix(line, "@"+host) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	if len(kept) == 0 {
		return os.Remove(credFile)
	}
	_, err = atomicfile.WriteFile(ctx, credFile,
		[]byte(strings.Join(kept, "\n")+"\n"), atomicfile.WithMode(0o600))
	return err
}
