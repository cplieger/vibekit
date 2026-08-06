// Package redact removes credential-shaped substrings from text before it is
// persisted, re-served, or exported.
//
// The problem this solves: vibekit stores what the agent's tools printed. If a
// tool ever echoes a credential — `env`, a `curl -v`, a misconfigured script —
// that value lands in the chat file verbatim, is re-served by the chat API on
// every load, and is embedded in any markdown export the user shares. Stripping
// ANSI escapes and hidden Unicode (see [github.com/cplieger/vibekit/internal/api.SanitizeOutput])
// does not touch it.
//
// The approach is deliberately narrow: match only credential shapes that carry
// an unmistakable issuer prefix, and leave everything else alone. That trades
// recall for precision on purpose. Agent output is read by a human who needs it
// to be accurate, so a redactor that mangles a base64 blob, a JWT or a `.env`
// line to catch a hypothetical secret costs more than it saves. There is no
// entropy heuristic here and that is a design decision, not an omission.
//
// This package has no dependencies beyond the standard library, so it can be
// lifted into a shared module if a second app needs it.
package redact

import (
	"encoding/json"
	"regexp"
)

// secretKVRe matches a JSON string key whose name looks secret-ish and
// captures the `"key":` prefix (group 1) so the value can be replaced with a
// placeholder. Go's regexp is RE2 (linear time), so this is ReDoS-safe.
//
// Key-driven rather than shape-driven, so it catches secrets with no
// recognizable form — at the cost of blanking any value under a matching key,
// which is why [Report] uses it and [Output] does not.
var secretKVRe = regexp.MustCompile(
	`(?i)("[^"\r\n]*(?:token|secret|password|passwd|authorization|credential|` +
		`api[_-]?key|access[_-]?key|secret[_-]?key|session[_-]?token|` +
		`private[_-]?key|client[_-]?secret|bearer)[^"\r\n]*"\s*:\s*)"[^"]*"`)

// secretTokenRe matches unambiguous standalone secret token shapes: AWS
// access-key ids, common provider PATs (GitHub/GitLab/Slack), and Bearer auth
// headers. Kept tight to unmistakable prefixes so benign values like commit
// hashes are not redacted.
var secretTokenRe = regexp.MustCompile(
	`(?i)\b(?:AKIA|ASIA)[0-9A-Z]{16}\b` +
		`|\bgh[opsur]_[A-Za-z0-9]{20,}\b` +
		`|\bgithub_pat_[A-Za-z0-9_]{20,}\b` +
		`|\bglpat-[A-Za-z0-9_-]{16,}\b` +
		`|\bxox[baprs]-[A-Za-z0-9-]{10,}\b` +
		`|bearer\s+[A-Za-z0-9._~+/-]{8,}={0,2}`)

// Placeholder replaces a redacted value. Exported so callers and tests can
// assert on it without restating the literal.
const Placeholder = "[redacted]"

// Output masks credential-shaped tokens in text a human will read: agent tool
// output, a streamed delta, an exported transcript.
//
// Token shapes only. It deliberately does NOT apply the secret-named-field
// rule that [Report] uses, because agent output legitimately contains JSON
// whose values the user needs to see — a config dump, an API response, a
// terraform plan. Blanking every value under a key merely NAMED "token" would
// destroy readable output to defend against a secret that may not be there.
//
// Safe on structured text: every pattern matches a run of unquoted characters,
// so a replacement inside a JSON string value cannot unbalance the document.
func Output(s string) string {
	return secretTokenRe.ReplaceAllString(s, Placeholder)
}

// Report masks credential-shaped tokens AND the values of secret-named fields.
//
// For bounded diagnostic text that is machine-shaped rather than read for its
// content — a `kiro-cli diagnostic` dump, a support bundle. Over-redaction is
// the right failure here: nobody is reading the report for the value of a
// field called "client_secret".
//
// Being pattern-based, this is a defense-in-depth layer and not a guarantee.
func Report(s string) string {
	s = secretKVRe.ReplaceAllString(s, `${1}"`+Placeholder+`"`)
	return secretTokenRe.ReplaceAllString(s, Placeholder)
}

// RawJSON masks credential-shaped tokens in a raw JSON document, for a tool
// call's INPUT — the arguments the agent passed, which are persisted, re-served
// and exported exactly like its output.
//
// Input is the likelier leak of the two: a credential in output means the agent
// printed one, while a credential in input means it ran something like
// `curl -H "Authorization: Bearer …"`, which is ordinary.
//
// Structurally safe for the same reason [Output] is, and it matters more here
// because the result must stay parseable: every pattern matches a run of
// characters that cannot contain a quote, so a replacement inside a string value
// cannot unbalance the document. Nil and empty input pass through untouched.
//
// This is [Output] applied to bytes, not a second rule set — one redactor, one
// vocabulary, so output and input cannot drift apart in what they mask.
func RawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	out := Output(string(raw))
	if out == string(raw) {
		// Unchanged: hand back the original slice rather than a copy.
		return raw
	}
	return json.RawMessage(out)
}
