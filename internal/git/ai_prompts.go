package git

import (
	"log/slog"
	"strings"
	"text/template"

	"vibekit/internal/api"
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
	msg = api.StripCodeFence(msg)

	// Strip "COMMIT MESSAGE:" prefix (case-insensitive).
	const commitPrefix = "COMMIT MESSAGE:"
	if len(msg) >= len(commitPrefix) && strings.EqualFold(msg[:len(commitPrefix)], commitPrefix) {
		msg = msg[len(commitPrefix):]
	}
	msg = strings.TrimSpace(msg)

	// Strip surrounding quotes (iteratively, trimming between layers so an
	// interior space like `""" "` cannot halt stripping; keeps it idempotent).
	for {
		msg = strings.TrimSpace(msg)
		if len(msg) >= 2 && ((msg[0] == '"' && msg[len(msg)-1] == '"') ||
			(msg[0] == '\'' && msg[len(msg)-1] == '\'')) {
			msg = msg[1 : len(msg)-1]
		} else {
			break
		}
	}

	// Split into subject and body.
	lines := strings.SplitN(msg, "\n", 2)
	subject := strings.TrimSpace(lines[0])

	// Cap subject at 72 chars. Prefer breaking at a word boundary past
	// column 30 so short subjects aren't silently truncated mid-word.
	if len(subject) > 72 {
		if idx := strings.LastIndex(subject[:69], " "); idx > 30 {
			subject = subject[:idx] + "..."
		} else {
			subject = subject[:69] + "..."
		}
	}

	if len(lines) < 2 {
		return subject
	}

	body := strings.TrimSpace(lines[1])
	if body == "" {
		return subject
	}

	return subject + "\n\n" + body
}
