// Package git provides HTTP handlers and utilities for git operations and AI-assisted workflows.
package git

import (
	"log/slog"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/modeltext"
)

// Subject-line shaping bounds, in RUNES. 72 is the conventional git subject
// length; the ellipsis is reserved out of it so a capped subject is never longer
// than the cap, which the byte-slicing version this replaced got wrong by three.
const (
	subjectMaxRunes     = 72
	subjectWordBreakMin = 30
	subjectEllipsis     = "..."
)

// commitPrefixes is the canonical list of allowed commit-message type
// prefixes. Referenced by the prompt template and available for future
// commit-message validation.
var commitPrefixes = [...]string{
	"feat", "fix", "sec", "refactor", "chore", "docs", "test", "perf", "ci",
}

// promptKind is a typed key for the promptTemplates map. Using a named
// type instead of bare strings makes typos a compile error.
type promptKind string

const (
	promptCommit promptKind = "commit"
	promptPR     promptKind = "pr"
	promptBranch promptKind = "branch"
)

// promptTemplates holds the AI prompt templates keyed by purpose.
var promptTemplates = map[promptKind]*template.Template{
	promptCommit: template.Must(template.New("commit").Parse(`Generate a Git commit message. Return ONLY the message, no fences.

FORMAT:
<type>(<scope>): <subject max 72 chars, imperative mood>

- Bullet point per logical change
- Use imperative mood ("Add", "Fix", not "Added", "Fixed")

PREFIXES (must match one):
{{- range .Prefixes}}
  {{.}}
{{- end}}

SCOPE: the app or module name (e.g. "vibekit", "subflux", "age").
For multi-app changes use comma-separated scopes.

RECENT COMMITS (match the style):
{{.CommitHistory}}

STAGED DIFF:
{{.Diff}}

Respond with only the commit message:`)),

	promptBranch: template.Must(template.New("branch").Parse(`Suggest a git branch name for the work in progress below. Return ONLY the branch name.

RULES:
- kebab-case, lowercase, max 40 characters
- start with a conventional type prefix and a slash: feat/ fix/ refactor/ chore/ docs/ test/ ci/
- describe the change, not the files (e.g. "feat/tab-status-badges", not "feat/update-tabs-ts")
- no spaces, no special characters beyond - / .

EXISTING BRANCHES (avoid collisions, match the style):
{{.Branches}}

WORK IN PROGRESS:
{{.Context}}

Respond with only the branch name:`)),

	promptPR: template.Must(template.New("pr").Parse(`Generate a pull request description for the following changes. Return ONLY the description text.

FORMAT:
## Summary
One paragraph describing what this PR does and why.

## Changes
- Bullet point list of specific changes

## Testing
How to verify these changes work.

COMMITS IN THIS BRANCH:
{{.Log}}

DIFF:
{{.Diff}}

Generate the PR description:`)),
}

// buildCommitPrompt constructs the AI prompt for commit message generation.
// Template execution failure is logged and the partial buffer is returned —
// promptTemplates are validated at init via template.Must(), so the expected
// failure modes are all "closed" (callers still get a usable prompt, just
// possibly with a missing section).
func buildCommitPrompt(commitHistory, fullDiff string) string {
	var b strings.Builder
	if err := promptTemplates[promptCommit].Execute(&b, map[string]any{
		"Prefixes":      commitPrefixes[:],
		"CommitHistory": commitHistory,
		"Diff":          fullDiff,
	}); err != nil {
		slog.Warn("git: commit prompt template execute failed", "error", err)
	}
	return b.String()
}

// buildPRPrompt constructs the AI prompt for PR description generation.
// See buildCommitPrompt for the error-handling rationale.
func buildPRPrompt(log, diff string) string {
	var b strings.Builder
	if err := promptTemplates[promptPR].Execute(&b, map[string]any{
		"Log":  log,
		"Diff": diff,
	}); err != nil {
		slog.Warn("git: pr prompt template execute failed", "error", err)
	}
	return b.String()
}

// extractCommitMessage cleans up raw model output into a proper commit message.
// Strips markdown fences, "COMMIT MESSAGE:" prefix, surrounding quotes.
// Formats bullet-point bodies. Caps subject line at 72 chars.
func extractCommitMessage(raw string) string {
	msg := strings.TrimSpace(raw)
	// Strip markdown fences (including language-tagged variants like
	// ```go / ```markdown / ```diff).
	msg = modeltext.StripCodeFence(msg)
	msg = stripCommitPrefix(msg)
	msg = stripSurroundingQuotes(msg)

	// Split into subject and body.
	lines := strings.SplitN(msg, "\n", 2)
	subject := capSubject(strings.TrimSpace(lines[0]))

	if len(lines) < 2 {
		return subject
	}

	body := strings.TrimSpace(lines[1])
	if body == "" {
		return subject
	}

	return subject + "\n\n" + body
}

// stripCommitPrefix removes any number of leading case-insensitive
// "COMMIT MESSAGE:" markers, trimming whitespace between layers so a
// stacked "COMMIT MESSAGE:COMMIT MESSAGE:..." is fully stripped (idempotent).
func stripCommitPrefix(msg string) string {
	const commitPrefix = "COMMIT MESSAGE:"
	for {
		msg = strings.TrimSpace(msg)
		if len(msg) >= len(commitPrefix) && strings.EqualFold(msg[:len(commitPrefix)], commitPrefix) {
			msg = msg[len(commitPrefix):]
		} else {
			return msg
		}
	}
}

// stripSurroundingQuotes removes matched surrounding single or double
// quotes iteratively, trimming whitespace between layers so an interior
// space like `""" "` cannot halt stripping. Idempotent.
func stripSurroundingQuotes(msg string) string {
	for {
		msg = strings.TrimSpace(msg)
		if len(msg) >= 2 && ((msg[0] == '"' && msg[len(msg)-1] == '"') ||
			(msg[0] == '\'' && msg[len(msg)-1] == '\'')) {
			msg = msg[1 : len(msg)-1]
		} else {
			return msg
		}
	}
}

// capSubject caps the subject line at subjectMaxRunes. It prefers breaking at a
// word boundary past subjectWordBreakMin so short subjects aren't silently
// truncated mid-word.
//
// The unit is RUNES, not bytes. It used to slice bytes while its doc said
// "chars", which for a non-ASCII subject cut a multi-byte rune in half and
// produced invalid UTF-8 — the JSON encoder then replaced the fragment with
// U+FFFD, so the user saw a replacement character in a suggested commit message.
// The word-break threshold moved to the same unit for the same reason: a byte
// index compared against 30 means a different thing in each script.
//
// strings.CutLast (1.27) replaces a strings.LastIndex plus a slice at the index
// it returned. The site qualifies on the rule that decides most of them: the
// separator is a single LITERAL (" "), and `before` is exactly the prefix the
// old arithmetic computed. It is not the "last whitespace-separated field" shape
// that strings.Fields owns — a tab in a commit subject is not a word break this
// function has ever honoured, and treating it as one would be a behaviour change
// dressed as a modernization.
func capSubject(subject string) string {
	if utf8.RuneCountInString(subject) <= subjectMaxRunes {
		return subject
	}
	head := truncateRunes(subject, subjectMaxRunes-len(subjectEllipsis))
	if before, _, found := strings.CutLast(head, " "); found &&
		utf8.RuneCountInString(before) > subjectWordBreakMin {
		return before + subjectEllipsis
	}
	return head + subjectEllipsis
}

// truncateRunes returns the first n runes of s, whole. n is assumed
// non-negative; every caller derives it from a positive constant.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// buildBranchPrompt constructs the AI prompt for branch-name suggestion.
// See buildCommitPrompt for the error-handling rationale.
func buildBranchPrompt(branches, context string) string {
	var b strings.Builder
	if err := promptTemplates[promptBranch].Execute(&b, map[string]any{
		"Branches": branches,
		"Context":  context,
	}); err != nil {
		slog.Warn("git: branch prompt template execute failed", "error", err)
	}
	return b.String()
}

// sanitizeBranchName normalizes raw model output into a safe git branch
// name: lowercase kebab-case restricted to [a-z0-9./-], runs of anything
// else collapsed to single dashes, dashes/dots/slashes trimmed at segment
// edges, capped at 60 bytes. Returns "" when nothing usable remains.
func sanitizeBranchName(raw string) string {
	s := strings.TrimSpace(raw)
	s = modeltext.StripCodeFence(s)
	s = stripSurroundingQuotes(s)
	// First line only: models sometimes add an explanation line.
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '/' || r == '.':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == ' ' || r == '_':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// drop anything exotic without emitting a dash run
		}
	}
	out := cleanBranchSegments(b.String())
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-./")
	}
	return out
}

// cleanBranchSegments trims junk at segment boundaries: no leading/trailing
// separators, no empty segments, no ".." (git ref rules).
func cleanBranchSegments(s string) string {
	segs := strings.Split(s, "/")
	clean := segs[:0]
	for _, seg := range segs {
		seg = strings.Trim(seg, "-.")
		if seg == "" || strings.Contains(seg, "..") {
			continue
		}
		clean = append(clean, seg)
	}
	return strings.Join(clean, "/")
}
