package mcp

// Pasting a publisher's block.
//
// Every MCP server's README hands out a JSON block in the Claude-Desktop / KAS
// family shape — a MAP of servers under one wrapper key — and the only way in
// used to be retyping it field by field, once per server. This file translates
// that block into vibekit's own records.
//
// The translation is the INVERSE of kasfile.go's renderKASServers, deliberately:
// reading the renderer backwards is the cheapest correctness check available,
// and it is where the transport-inference rule is already written down (KAS
// infers the transport from which fields are present, accepts a `type` hint and
// ignores it).
//
// # Unknown keys are NAMED, not dropped
//
// encoding/json ignores a key with no matching field, so a typo like "comand"
// produced a server that did nothing, and the only error the user saw named
// `command` as missing — which is not the thing that was wrong. Blanket
// DisallowUnknownFields is the wrong tool: a real publisher block legitimately
// carries keys vibekit has no field for (`cwd`, `timeout`, `waitForReady`), so
// strictness would refuse the paste rather than name the typo, which is the
// opposite of the point. It also cannot tell a typo from an unsupported field,
// and that distinction is the whole value. So keys are classified in three:
// consumed, known-but-unmodelled (accepted, reported as a note so `timeout`
// does not read as a typo), and unknown (400 naming the key with a nearest
// match).
//
// Not to be confused with kasfile.go's policy for the file vibekit WRITES,
// where unknown top-level keys are preserved in silence because KAS reads
// `powers.mcpServers` out of the same file. Different file, different
// direction.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	// maxImportServers bounds one paste. A README lists two or three servers;
	// a block naming more than this is not something to install in one gesture.
	maxImportServers = 32
	// importSuggestDistance is the edit-distance ceiling for a "did you mean"
	// hint. 2 catches a transposition or a dropped letter ("comand", "agrs")
	// without pairing unrelated short keys ("url" / "env" are distance 3).
	importSuggestDistance = 2
	// maxImportBlockKeys bounds the key count of any one object before it is
	// classified, so a hostile paste cannot make the sort dominate the request.
	maxImportBlockKeys = 128
)

// pasteServerKeys are the per-server keys the translator consumes. `name` is
// here because a single-server paste (the panel's own template) carries its
// name inside the object, where a block carries it as the map key.
var pasteServerKeys = []string{
	"args", "autoApprove", "command", "disabled", "disabledTools",
	"env", "headers", "name", "oauth", "prewarm", "type", "url",
}

// pasteServerIgnored are the per-server keys vibekit recognises and has nowhere
// to put. Each is accepted with a note naming why, so a block carrying one
// installs instead of erroring, and the user is not left wondering whether it
// was a typo. The reasons are the user's, not the schema's: "no field for it"
// is actionable, "unknown key" would not be.
var pasteServerIgnored = map[string]string{
	"$schema":      "a schema pointer, not configuration",
	"alwaysAllow":  `another client's spelling of "autoApprove" — rename it to carry it over`,
	"cwd":          "vibekit has no working-directory field",
	"description":  "not stored; the name is the label",
	"icon":         "not stored",
	"oauthScopes":  "vibekit has no OAuth scope field",
	"timeout":      "vibekit has no timeout field; the agent sets its own",
	"waitForReady": "vibekit has no wait-for-ready field",
}

// pasteOAuthKeys are the keys of a server's nested `oauth` object — the whole
// set, because KAS's own schema carries exactly these two (a scope list is the
// sibling `oauthScopes`, classified above, not a member here).
//
// The nested object needs its own classification pass: encoding/json drops a
// key with no matching field, so without one a misspelt `clientSecrect` was
// accepted, discarded, and stored as an EMPTY secret — silently, at the one
// boundary where the input is copied out of somebody else's README.
var pasteOAuthKeys = []string{"clientId", "clientSecret"}

// pasteTopKeys are the top-level keys of a pasted block. Only the wrapper is
// consumed; a single-server object is detected by the wrapper's absence.
var pasteTopKeys = []string{kasServerKey}

// pasteTopIgnored are top-level keys other clients' config files carry around
// an mcpServers block.
var pasteTopIgnored = map[string]string{
	"$schema": "a schema pointer, not configuration",
	"inputs":  "an editor's input-prompt list; type the values into the form instead",
}

// importRequest is one parsed paste: the records to create, in the order the
// block declared them, plus the notes the translation produced (an unmodelled
// key, a name that had to be adjusted).
type importRequest struct {
	servers []*Server
	notes   []string
}

// pasteOAuth is the publisher shape of a pre-registered OAuth client, the
// inverse of kasfile.go's kasOAuth.
type pasteOAuth struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// pasteServer is the publisher-shaped server object. `env` and `headers` are
// absent on purpose: they are JSON records, and vibekit stores ordered
// KeyPairs, so they are decoded from the raw bytes to keep the README's order
// (a Go map would discard it, and the order is what the user reads in the form).
type pasteServer struct {
	OAuth         *pasteOAuth `json:"oauth"`
	Command       *string     `json:"command"`
	URL           *string     `json:"url"`
	Type          *string     `json:"type"`
	Name          *string     `json:"name"`
	Disabled      *bool       `json:"disabled"`
	Prewarm       *bool       `json:"prewarm"`
	Args          []string    `json:"args"`
	DisabledTools []string    `json:"disabledTools"`
	AutoApprove   []string    `json:"autoApprove"`
}

// parseImportBody translates a pasted body into records ready for the store.
// Two shapes are accepted, because a user pastes whatever the README gave them:
// a `mcpServers` block (one or more servers, keyed by name) or a single server
// object carrying its own `name`.
func parseImportBody(data []byte) (*importRequest, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("body must be a JSON object: %w", err)
	}
	if len(doc) == 0 {
		return nil, errors.New(`empty object: paste the "mcpServers" block from the server's README`)
	}
	// Non-nil so the response carries [] rather than null: the client's type says
	// an array, and one shape on the wire beats two the reader has to handle.
	req := &importRequest{notes: []string{}}
	block, isBlock := doc[kasServerKey]
	if !isBlock {
		return parseSingleServer(doc, data, req)
	}
	if err := classifyKeys("", doc, pasteTopKeys, pasteTopIgnored, req); err != nil {
		return nil, err
	}
	return parseServerBlock(block, req)
}

// parseSingleServer handles the one-object shape: the whole body is a server
// and its name is a field on it.
func parseSingleServer(doc map[string]json.RawMessage, raw json.RawMessage, req *importRequest) (*importRequest, error) {
	var named struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &named); err != nil {
		return nil, errors.New(`"name" must be a string`)
	}
	if strings.TrimSpace(named.Name) == "" {
		return nil, errors.New(`missing "name": paste an "mcpServers" block, or a single server object with a name`)
	}
	sv, err := translateServer(named.Name, doc, raw, req)
	if err != nil {
		return nil, err
	}
	req.servers = append(req.servers, sv)
	return req, nil
}

// parseServerBlock handles the `mcpServers` map shape. Entries are translated
// in the block's own key order (sorted, since a JSON object has none once
// decoded) so a re-paste of the same block produces the same order.
func parseServerBlock(block json.RawMessage, req *importRequest) (*importRequest, error) {
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(block, &entries); err != nil {
		return nil, fmt.Errorf(`"%s" must be a JSON object of servers keyed by name: %w`, kasServerKey, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf(`"%s" is empty: no servers to connect`, kasServerKey)
	}
	if len(entries) > maxImportServers {
		return nil, fmt.Errorf(`"%s" names %d servers (max %d)`, kasServerKey, len(entries), maxImportServers)
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(entries[key], &obj); err != nil {
			return nil, fmt.Errorf("server %q: must be a JSON object: %w", key, err)
		}
		sv, err := translateServer(key, obj, entries[key], req)
		if err != nil {
			return nil, err
		}
		req.servers = append(req.servers, sv)
	}
	return req, nil
}

// translateServer maps one publisher server object onto a vibekit record.
// Validate is NOT called here: the store calls it on every record it takes, and
// running it twice would report the same problem in two voices.
func translateServer(rawName string, obj map[string]json.RawMessage, raw json.RawMessage, req *importRequest) (*Server, error) {
	name, nameErr := importName(rawName, req)
	if nameErr != nil {
		return nil, nameErr
	}
	where := fmt.Sprintf("server %q: ", name)
	if keyErr := classifyKeys(where, obj, pasteServerKeys, pasteServerIgnored, req); keyErr != nil {
		return nil, keyErr
	}
	// Classified before the decode below, and regardless of transport: the outer
	// pass only sees that `oauth` is a consumed key, so the object's own members
	// would otherwise reach json.Unmarshal's silent-drop.
	if oauthErr := classifyOAuthKeys(where, obj["oauth"], req); oauthErr != nil {
		return nil, oauthErr
	}
	var spec pasteServer
	if decErr := json.Unmarshal(raw, &spec); decErr != nil {
		return nil, fmt.Errorf("%s%w", where, decErr)
	}
	transport, err := transportFor(&spec)
	if err != nil {
		return nil, fmt.Errorf("%s%w", where, err)
	}
	sv := &Server{
		Name:          name,
		Transport:     transport,
		Args:          spec.Args,
		DisabledTools: spec.DisabledTools,
		AutoApprove:   spec.AutoApprove,
		// A publisher block spells the flag the other way round, and vibekit's
		// own default is on: a server nobody switched off is one the user just
		// asked for.
		Enabled: spec.Disabled == nil || !*spec.Disabled,
	}
	switch transport {
	case TransportStdio:
		sv.Command = strings.TrimSpace(*spec.Command)
		if sv.Env, err = decodeOrderedPairs(where+"env", obj["env"]); err != nil {
			return nil, err
		}
		sv.Prewarm = prewarmFor(&spec, sv.Command)
	case TransportHTTP, TransportSSE:
		sv.URL = strings.TrimSpace(*spec.URL)
		if sv.Headers, err = decodeOrderedPairs(where+"headers", obj["headers"]); err != nil {
			return nil, err
		}
		if spec.OAuth != nil {
			sv.OAuthClientID = strings.TrimSpace(spec.OAuth.ClientID)
			sv.OAuthClientSecret = strings.TrimSpace(spec.OAuth.ClientSecret)
		}
	}
	return sv, nil
}

// transportFor infers the transport the way KAS does: from which fields are
// present. `type` is advisory — KAS accepts it and ignores it — so it decides
// only http-versus-sse for a remote server, and an unrecognised value falls
// through to http rather than failing, which is what KAS's own negotiation does.
func transportFor(spec *pasteServer) (Transport, error) {
	cmd := strings.TrimSpace(deref(spec.Command))
	remote := strings.TrimSpace(deref(spec.URL))
	switch {
	case cmd != "" && remote != "":
		return "", errors.New(`has both "command" and "url"; a server is either local (command) or hosted (url)`)
	case cmd != "":
		return TransportStdio, nil
	case remote != "":
		if t, ok := supportedRemoteTypes[strings.ToLower(deref(spec.Type))]; ok {
			return t, nil
		}
		return TransportHTTP, nil
	default:
		return "", errors.New(`needs either "command" (a local server) or "url" (a hosted one)`)
	}
}

// prewarmFor mirrors the npm form's default: an npx server pays an install cost
// on the first chat after a container start unless it is pre-installed, and
// nothing else is prewarm-eligible (see prewarm.extractNpxPackage). An explicit
// flag in the block wins.
func prewarmFor(spec *pasteServer, command string) bool {
	if spec.Prewarm != nil {
		return *spec.Prewarm
	}
	return command == "npx"
}

// importName maps a block key (or a single object's "name") onto a name the
// store will accept. An adjustment is REPORTED rather than made silently: the
// name becomes the agent's tool prefix (mcp_<name>_<tool>), so a user whose
// README said one thing and whose tool list says another needs to be told.
func importName(raw string, req *importRequest) (string, error) {
	trimmed := strings.TrimSpace(raw)
	clean := sanitizeName(trimmed)
	if clean == "" {
		return "", fmt.Errorf("server %q: name needs at least one letter", trimmed)
	}
	if clean != trimmed {
		req.notes = append(req.notes,
			fmt.Sprintf("named %q %q: a name starts with a letter and holds letters, digits, %q and %q",
				trimmed, clean, "_", "-"))
	}
	return clean, nil
}

// sanitizeName folds a raw name into the shared grammar: anything outside
// NameAllowedRune becomes "-", the result is trimmed to open on a lead rune, and
// it is capped at NameMaxLen.
//
// This is a REPAIRER, not a rejector — it is what lets a README's `@scope/pkg`
// install instead of erroring — but its charset and its bound are validate.go's,
// not its own. It used to restate both as a switch and a literal 64, with three
// comments claiming they matched a regexp that no longer exists; that regexp was
// the third copy D81 exists to end. TestSanitizeNameAlwaysValid asserts the postcondition mechanically: every
// output ValidateName accepts.
func sanitizeName(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if NameAllowedRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.TrimFunc(b.String(), func(r rune) bool { return !NameLeadRune(r) })
	// TrimFunc only strips the ends, so the leading rune is now a lead rune (or
	// the string is empty). Trailing separators went with it, which is the shape
	// the grammar wants anyway. The cap is applied last and is byte-safe: every
	// kept rune is single-byte ASCII by NameAllowedRune's construction.
	if len(out) > NameMaxLen {
		out = out[:NameMaxLen]
	}
	return out
}

// classifyOAuthKeys runs the same three-way classification over a server's
// nested `oauth` object, so a typo there is NAMED like every other one instead
// of being dropped into an empty credential. Absent or null yields nothing to
// classify; a non-object is named here rather than surfacing later as a decode
// error about the whole server.
//
// There is no ignored set: both of KAS's oauth members are consumed, so any
// other key really is a typo.
func classifyOAuthKeys(where string, raw json.RawMessage, req *importRequest) error {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf(`%s"oauth" must be a JSON object: %w`, where, err)
	}
	return classifyKeys(where+"oauth: ", obj, pasteOAuthKeys, nil, req)
}

// classifyKeys splits an object's keys into the ones the translator consumes,
// the ones it recognises and cannot store (recorded as a note), and the rest —
// which is a typo, and is named. `where` prefixes the message with the server
// it came from; it is empty at the top level.
func classifyKeys(where string, obj map[string]json.RawMessage, consumed []string, ignored map[string]string, req *importRequest) error {
	if len(obj) > maxImportBlockKeys {
		return fmt.Errorf("%s%d keys (max %d)", where, len(obj), maxImportBlockKeys)
	}
	unknown := make([]string, 0, len(obj))
	notes := make([]string, 0, len(obj))
	for key := range obj {
		switch {
		case slices.Contains(consumed, key):
		case ignored[key] != "":
			notes = append(notes, fmt.Sprintf("%signoring %q: %s", where, key, ignored[key]))
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		// Sorted so a block with several typos names the same one every time;
		// a caller fixing them one at a time needs a stable answer.
		slices.Sort(unknown)
		return fmt.Errorf("%sunknown key %q%s", where, unknown[0],
			suggestKey(unknown[0], consumed, ignored))
	}
	slices.Sort(notes)
	req.notes = append(req.notes, notes...)
	return nil
}

// suggestKey returns a parenthesised "did you mean" for the nearest known key,
// or "" when nothing is close. This is what turns "comand" from a rejection
// into a fix.
func suggestKey(got string, consumed []string, ignored map[string]string) string {
	lower := strings.ToLower(got)
	best, bestDist := "", importSuggestDistance+1
	candidates := make([]string, 0, len(consumed)+len(ignored))
	candidates = append(candidates, consumed...)
	for key := range ignored {
		candidates = append(candidates, key)
	}
	slices.Sort(candidates)
	for _, cand := range candidates {
		if d := editDistance(lower, strings.ToLower(cand)); d < bestDist {
			best, bestDist = cand, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// editDistance is Levenshtein over two short ASCII-ish keys, computed with one
// rolling row. Only ever called on JSON object keys, so the quadratic cost is
// bounded by maxImportBlockKeys and the key length cap the body limit implies.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// decodeOrderedPairs reads a JSON record into ordered KeyPairs, preserving the
// document's order. Absent yields nil. A scalar value is stringified — a real
// block writes `"PORT": 3000` — while an object, array or null is a mistake
// worth naming rather than flattening.
func decodeOrderedPairs(field string, raw json.RawMessage) ([]KeyPair, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("%s must be a JSON object of name/value pairs", field)
	}
	var out []KeyPair
	for dec.More() {
		keyTok, kErr := dec.Token()
		if kErr != nil {
			return nil, fmt.Errorf("%s: %w", field, kErr)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%s: non-string key", field)
		}
		var val any
		if vErr := dec.Decode(&val); vErr != nil {
			return nil, fmt.Errorf("%s[%q]: %w", field, key, vErr)
		}
		text, ok := scalarString(val)
		if !ok {
			return nil, fmt.Errorf("%s[%q]: value must be a string, number or boolean", field, key)
		}
		out = append(out, KeyPair{Name: key, Value: text})
	}
	return out, nil
}

// scalarString renders a decoded JSON scalar as the string vibekit stores. The
// bool reports whether the value was a scalar at all.
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

// deref reads through an optional string field, treating absent as empty.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
