// Package kiroauth reads (and refreshes) the ambient kiro-cli SSO access
// token so vibekit can answer the v3 (KAS) host-mediated auth callback
// (_kiro/auth/getAccessToken) over the ACP bridge.
//
// kiro-cli acp --agent-engine v3 runs with --auth=acp-callback: the agent
// asks the CLIENT for the bearer token instead of reading its own store,
// and it REJECTS any token inside a ~180s expiry buffer (TokenInvalidError)
// — the host is expected to refresh. So this package both reads and, when
// the token nears expiry, refreshes it via the standard AWS SSO-OIDC
// CreateToken (refresh_token grant) and writes the rotated token back to
// the AWS SSO cache atomically, mirroring what ambient kiro-cli does. That
// keeps a single cooperative token store rather than forking a private
// refresh chain.
//
// Token file:  ~/.aws/sso/cache/kiro-auth-token.json
//
//	{accessToken, refreshToken, expiresAt, clientIdHash, region, ...}
//
// Registration: ~/.aws/sso/cache/<clientIdHash>.json {clientId, clientSecret}
package kiroauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cplieger/runesafe"
)

// RefreshLeeway matches KAS's TOKEN_REFRESH_LEEWAY (~180s): a token
// expiring within this window is refreshed before vending. Slightly wider
// than KAS's own buffer so we refresh before KAS would reject.
const RefreshLeeway = 5 * time.Minute

// authMethodIDC is the tokenFile.authMethod value for IAM Identity Center
// logins. KAS dispatches token refresh on this field
// (switch(authMethod){case "IdC":refreshIdC; case "social":refreshSocial;
// case "external_idp":refreshExternalIdp; default: reject}). The SSO-OIDC
// CreateToken (refresh_token grant) that refreshLocked performs is the
// refreshIdC shape ONLY — so it must be gated on this exact value.
const authMethodIDC = "IdC"

// tokenFile is the TYPED read view of the fields in
// ~/.aws/sso/cache/kiro-auth-token.json that the refresh logic needs. It is
// deliberately NOT the write shape: the file may carry additional keys (a
// non-IdC login, or a field a future kiro-cli/AWS release adds), and
// narrowing the write-back to this struct would silently drop them and
// could corrupt the user's real login. Write-back therefore round-trips the
// raw JSON (map[string]json.RawMessage) and overwrites only the rotated keys
// — see persistLocked / applyRotatedFields.
type tokenFile struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresAt    string `json:"expiresAt"`
	ClientIDHash string `json:"clientIdHash,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Region       string `json:"region,omitempty"`
}

// registration is the OIDC client registration written by the SSO login.
type registration struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// Reader loads + refreshes the SSO access token. The mutex serializes all
// access so concurrent getAccessToken callbacks (one per bridge) never
// double-refresh and invalidate the rotating refresh token.
type Reader struct {
	cachedMtime time.Time
	httpClient  *http.Client
	tokenURL    func(region string) string
	cachedRaw   map[string]json.RawMessage
	cached      tokenFile
	path        string
	mu          sync.Mutex
	loaded      bool
	// dynamicPath re-resolves the token path on every load (constructed
	// with an empty path). Keeps the reader tracking whichever candidate
	// file the latest login wrote.
	dynamicPath bool
}

// tokenFileCandidates are the SSO-cache token file names kiro tooling
// writes, in preference order when mtimes tie. `kiro-auth-token-cli.json`
// is what a `kiro-cli login` stores (the only login source inside the
// vibekit container); `kiro-auth-token.json` is the name the Kiro IDE and
// KAS's own FileAuthProvider default to. Both shapes are identical.
// Hard-coding only the unsuffixed name broke production: the container's
// cache held only the -cli file, so every v3 getAccessToken callback
// failed with ENOENT and no session could start.
var tokenFileCandidates = []string{"kiro-auth-token-cli.json", "kiro-auth-token.json"}

// DefaultTokenPath returns the SSO cache token path under HOME (in the
// vibekit container HOME=/config/home): the freshest existing candidate
// file, or the CLI-name default when none exists yet. Empty if HOME can't
// be resolved.
func DefaultTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return resolveTokenPath(filepath.Join(home, ".aws", "sso", "cache"))
}

// resolveTokenPath picks the token file inside an SSO cache dir: the
// existing candidate with the newest mtime (a re-login rewrites its own
// name, so the freshest file is the live login), falling back to the
// primary candidate's path when none exists (the caller's stat then
// produces a clear not-found error).
func resolveTokenPath(dir string) string {
	best := ""
	var bestMtime time.Time
	for _, name := range tokenFileCandidates {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if best == "" || fi.ModTime().After(bestMtime) {
			best = filepath.Join(dir, name)
			bestMtime = fi.ModTime()
		}
	}
	if best != "" {
		return best
	}
	return filepath.Join(dir, tokenFileCandidates[0])
}

// NewReader returns a Reader for the given token path. An empty path
// enables dynamic resolution: every load re-runs DefaultTokenPath so a
// login that lands in the other candidate file (or a first login after
// vibekit booted logged-out) is picked up without a restart.
func NewReader(path string) *Reader {
	return &Reader{
		path:        path,
		dynamicPath: path == "",
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (r *Reader) endpoint(region string) string {
	if r.tokenURL != nil {
		return r.tokenURL(region)
	}
	if region == "" {
		region = "us-east-1"
	}
	return "https://oidc." + region + ".amazonaws.com/token"
}

// Token returns a currently-valid access token and its ISO-8601 expiry,
// refreshing (and writing back) when the cached token is within
// RefreshLeeway of expiry. On refresh failure it logs and returns the
// stale token best-effort (KAS may still reject it, but that surfaces as
// a clear auth error rather than a vibekit crash).
func (r *Reader) Token(ctx context.Context) (accessToken, expiresAt string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tf, raw, err := r.loadLocked()
	if err != nil {
		return "", "", err
	}
	if !NearExpiry(tf.ExpiresAt, RefreshLeeway) || tf.RefreshToken == "" {
		return tf.AccessToken, tf.ExpiresAt, nil
	}
	// refreshLocked speaks only the IAM Identity Center (SSO-OIDC
	// CreateToken) refresh_token grant. KAS routes other auth methods to
	// different endpoints/bodies (refreshSocial, refreshExternalIdp);
	// posting the IdC body for one of those would be a WRONG refresh that
	// could rotate — and thereby invalidate — the refresh token of the
	// user's real login. So gate on authMethod: for anything other than
	// IdC, vend the current token best-effort and WARN. KAS may still
	// reject it inside its ~180s buffer, but that surfaces as a clear auth
	// error rather than a corrupted login.
	if tf.AuthMethod != authMethodIDC {
		slog.Warn("kiroauth: near-expiry token uses a non-IdC auth method; vending stale (SSO-OIDC refresh not applicable)",
			"auth_method", tf.AuthMethod, "expires_at", tf.ExpiresAt)
		return tf.AccessToken, tf.ExpiresAt, nil
	}

	newTF, rErr := r.refreshLocked(ctx, tf)
	if rErr != nil {
		slog.Warn("kiroauth: token refresh failed; vending stale token",
			"error", rErr, "expires_at", tf.ExpiresAt)
		return tf.AccessToken, tf.ExpiresAt, nil
	}
	if wErr := r.persistLocked(newTF, raw); wErr != nil {
		// Refresh succeeded but write-back failed: vend the fresh token
		// anyway (this process is good) and warn — a restart would fall
		// back to the stale on-disk token.
		slog.Warn("kiroauth: refreshed token write-back failed", "error", wErr)
		r.cached = *newTF
	}
	slog.Info("kiroauth: refreshed SSO token", "expires_at", newTF.ExpiresAt)
	return newTF.AccessToken, newTF.ExpiresAt, nil
}

// loadLocked reads + parses the token file, caching by modtime. It returns
// both the typed view (for the refresh decision) and the full raw key set
// (for a lossless write-back). The raw map is always an independent copy, so
// the caller may mutate it without disturbing the cache. Caller holds r.mu.
func (r *Reader) loadLocked() (*tokenFile, map[string]json.RawMessage, error) {
	if r.dynamicPath {
		if p := DefaultTokenPath(); p != r.path {
			r.path = p
			r.loaded = false // path flipped: the mtime cache keys the old file
		}
	}
	if r.path == "" {
		return nil, nil, errors.New("kiro auth token: HOME not resolvable")
	}
	fi, statErr := os.Stat(r.path)
	if statErr != nil {
		return nil, nil, fmt.Errorf("kiro auth token stat: %w", statErr)
	}
	if r.loaded && fi.ModTime().Equal(r.cachedMtime) {
		c := r.cached
		return &c, cloneRaw(r.cachedRaw), nil
	}
	data, readErr := os.ReadFile(r.path)
	if readErr != nil {
		return nil, nil, fmt.Errorf("kiro auth token read: %w", readErr)
	}
	var tf tokenFile
	if uErr := json.Unmarshal(data, &tf); uErr != nil {
		return nil, nil, fmt.Errorf("kiro auth token parse: %w", uErr)
	}
	raw := map[string]json.RawMessage{}
	if uErr := json.Unmarshal(data, &raw); uErr != nil {
		return nil, nil, fmt.Errorf("kiro auth token parse (raw): %w", uErr)
	}
	if tf.AccessToken == "" || tf.ExpiresAt == "" {
		return nil, nil, errors.New("kiro auth token: missing accessToken/expiresAt")
	}
	r.cached = tf
	r.cachedRaw = raw
	r.cachedMtime = fi.ModTime()
	r.loaded = true
	c := tf
	return &c, cloneRaw(raw), nil
}

// refreshLocked performs the SSO-OIDC refresh_token grant. Caller holds r.mu.
func (r *Reader) refreshLocked(ctx context.Context, tf *tokenFile) (*tokenFile, error) {
	reg, err := r.loadRegistration(tf.ClientIDHash)
	if err != nil {
		return nil, fmt.Errorf("load client registration: %w", err)
	}
	reqBody, _ := json.Marshal(map[string]string{
		"clientId":     reg.ClientID,
		"clientSecret": reg.ClientSecret,
		"grantType":    "refresh_token",
		"refreshToken": tf.RefreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint(tf.Region), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Cap the response body defensively.
	body := readAllCapped(resp.Body, 1<<20)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso-oidc token endpoint: HTTP %d: %s", resp.StatusCode, runesafe.SanitizeSingleLineBounded(string(body), 200))
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int    `json:"expiresIn"`
	}
	if uErr := json.Unmarshal(body, &out); uErr != nil {
		return nil, fmt.Errorf("parse token response: %w", uErr)
	}
	if out.AccessToken == "" {
		return nil, errors.New("sso-oidc token endpoint: empty accessToken")
	}
	newTF := *tf
	newTF.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		newTF.RefreshToken = out.RefreshToken // rotate
	}
	ttl := out.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	newTF.ExpiresAt = time.Now().Add(time.Duration(ttl) * time.Second).UTC().Format(time.RFC3339Nano)
	return &newTF, nil
}

// loadRegistration reads {clientId, clientSecret} from the SSO cache. It
// tries <clientIdHash>.json first, then falls back to scanning the cache
// dir for any file carrying both fields.
func (r *Reader) loadRegistration(clientIDHash string) (*registration, error) {
	dir := filepath.Dir(r.path)
	if clientIDHash != "" {
		if reg, ok := readRegistration(filepath.Join(dir, clientIDHash+".json")); ok {
			return reg, nil
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Base(r.path) == e.Name() {
			continue
		}
		if reg, ok := readRegistration(filepath.Join(dir, e.Name())); ok {
			return reg, nil
		}
	}
	return nil, fmt.Errorf("no client registration (clientId/clientSecret) in %s", dir)
}

func readRegistration(path string) (*registration, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var reg registration
	if json.Unmarshal(data, &reg) != nil || reg.ClientID == "" || reg.ClientSecret == "" {
		return nil, false
	}
	return &reg, true
}

// persistLocked atomically writes the refreshed token back to the SSO cache
// file. It round-trips the ORIGINAL raw JSON (base) and overwrites only the
// rotated keys, so every other field — known siblings and any key this
// package doesn't model — survives verbatim; mode 0600 is preserved and the
// parent directory is fsync'd so the rename is durable. Caller holds r.mu.
func (r *Reader) persistLocked(tf *tokenFile, base map[string]json.RawMessage) error {
	merged, err := applyRotatedFields(base, tf)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".kiro-auth-token-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return err
	}
	// fsync the parent directory so the rename (the atomic swap of the
	// rotated token) is durable across a crash — this is what
	// cplieger/atomicfile gives the chat store + checkpoints, and the SSO
	// token deserves the same. Best-effort: the rename already succeeded, so
	// a dir-fsync failure is a durability gap worth a WARN, not a reason to
	// fail a refresh that otherwise landed.
	if dirF, derr := os.Open(dir); derr == nil {
		if serr := dirF.Sync(); serr != nil {
			slog.Warn("kiroauth: parent-dir fsync after token write-back failed", "error", serr)
		}
		_ = dirF.Close()
	} else {
		slog.Warn("kiroauth: could not open parent dir to fsync after token write-back", "error", derr)
	}
	r.cached = *tf
	r.cachedRaw = merged
	if fi, err := os.Stat(r.path); err == nil {
		r.cachedMtime = fi.ModTime()
		r.loaded = true
	}
	return nil
}

// applyRotatedFields returns a copy of base with only the refresh-rotated
// keys overwritten from tf (accessToken, expiresAt, refreshToken). Every
// other key is preserved verbatim (as its original raw bytes), so the
// write-back never narrows the token file to tokenFile's field set — the
// core protection against corrupting the user's real login.
func applyRotatedFields(base map[string]json.RawMessage, tf *tokenFile) (map[string]json.RawMessage, error) {
	out := cloneRaw(base)
	for _, kv := range []struct{ key, val string }{
		{"accessToken", tf.AccessToken},
		{"expiresAt", tf.ExpiresAt},
		{"refreshToken", tf.RefreshToken},
	} {
		enc, err := json.Marshal(kv.val)
		if err != nil {
			return nil, err
		}
		out[kv.key] = enc
	}
	return out, nil
}

// cloneRaw deep-copies a raw-JSON key map (each RawMessage byte slice
// included) so a mutation never aliases the Reader's cached copy. A nil
// input yields a non-nil empty map.
func cloneRaw(m map[string]json.RawMessage) map[string]json.RawMessage {
	// Capacity hint is len(m) exactly: applyRotatedFields adds at most 3
	// keys after cloning, which at worst triggers one cheap grow. A
	// len(m)+N hint would be a size computation CodeQL flags as a possible
	// allocation overflow (go/allocation-size-overflow) for no real gain.
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		cp := make(json.RawMessage, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// NearExpiry reports whether the token expires within leeway of now. An
// unparseable timestamp is treated as near-expiry (conservative).
func NearExpiry(expiresAt string, leeway time.Duration) bool {
	t, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return true
	}
	return time.Until(t) <= leeway
}

func readAllCapped(r interface{ Read([]byte) (int, error) }, limit int) []byte {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < limit {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			return buf
		}
	}
	return buf
}
