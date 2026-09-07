package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/cplieger/vibekit/internal/procout"
)

const identityProbeTTL = time.Minute

// Identity tracks the account that owns live agent sessions. It is safe for
// concurrent use. The zero value is inert.
//
// Unlike Kiro Crew's stamp-only status poll, every observation acts on a
// change, so one shared baseline cannot consume a signal another path needs.
type Identity struct {
	retire func()
	probe  func(context.Context) (string, error)
	now    func() time.Time

	lastProbe   time.Time
	fingerprint string
	mu          sync.Mutex
	absent      bool
}

// NewIdentity returns an identity registrar backed by kiro-cli whoami.
// retire must be non-nil because silently missing it would leave live bridges
// attached to the previous account.
func NewIdentity(cliPath func() string, env func() []string, retire func()) *Identity {
	if retire == nil {
		panic("auth: identity retire callback is nil")
	}
	return &Identity{
		retire: retire,
		probe: func(ctx context.Context) (string, error) {
			return probeIdentity(ctx, cliPath, env)
		},
		now: time.Now,
	}
}

// Observe adopts fp and retires live sessions when a known identity changes.
// Empty means absent; it can trigger retirement but never replaces the last
// known baseline.
func (id *Identity) Observe(fp string) {
	if id == nil {
		return
	}
	changed := false
	id.mu.Lock()
	switch {
	case fp == "":
		if id.fingerprint != "" && !id.absent {
			changed = true
		}
		id.absent = true
	case id.fingerprint == "":
		id.fingerprint = fp
		id.absent = false
	case id.fingerprint == fp:
		id.absent = false
	default:
		id.fingerprint = fp
		id.absent = false
		changed = true
	}
	retire := id.retire
	id.mu.Unlock()
	if changed && retire != nil {
		retire()
	}
}

// EnsureCurrent probes at most once per identityProbeTTL and fails open when
// whoami cannot supply a readable identity.
func (id *Identity) EnsureCurrent(ctx context.Context) {
	if id == nil {
		return
	}
	nowFn := id.now
	if nowFn == nil {
		return
	}
	now := nowFn()
	id.mu.Lock()
	if !id.lastProbe.IsZero() && now.Sub(id.lastProbe) < identityProbeTTL {
		id.mu.Unlock()
		return
	}
	id.lastProbe = now
	probe := id.probe
	id.mu.Unlock()
	if probe == nil {
		return
	}

	fp, err := probe(ctx)
	if err != nil {
		slog.Debug("identity probe failed open", "error", err)
		return
	}
	id.Observe(fp)
}

func probeIdentity(ctx context.Context, cliPath func() string, env func() []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultConfig.WhoamiTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPath(), "whoami", "--format", "json") //nolint:gosec // G204: binary path from config
	boundChild(cmd)
	if env != nil {
		cmd.Env = env()
	}
	stdout := procout.NewBuffer(whoamiMaxOutput)
	cmd.Stdout = stdout
	cmd.Stderr = procout.NewBuffer(stderrCap)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("run whoami: %w", err)
	}
	info, err := whoamiInfo(stdout.Bytes())
	if err != nil {
		return "", fmt.Errorf("parse whoami: %w", err)
	}
	return identityFingerprint(info), nil
}

// identityFingerprint hashes the allowlisted identity fields, and ONLY those, so a
// field upstream adds cannot silently start retiring bridges. An identity with none
// of them fingerprints as absent.
func identityFingerprint(info WhoamiResponse) string {
	if info.Email == "" && info.AccountType == "" && info.StartURL == "" && info.Region == "" {
		return ""
	}
	allowed := "email\x00" + info.Email + "\x00" +
		"account_type\x00" + info.AccountType + "\x00" +
		"start_url\x00" + info.StartURL + "\x00" +
		"region\x00" + info.Region + "\x00"
	sum := sha256.Sum256([]byte(allowed))
	return string(sum[:])
}
