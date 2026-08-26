package vibekit

import (
	"slices"
	"strings"
)

// The hook trigger vocabulary: which trigger names KAS loads, and what each
// one's matcher is tested AGAINST.
//
// It lives here rather than beside either consumer because BOTH sides of the hook
// surface need it and they are in different packages. internal/command validates
// a create_hook payload against it; internal/agent computes a diagnostic for a
// hook KAS reports back. A copy in each would be two tables that can disagree
// about a trigger's subject, and the subject is the whole basis of the diagnostic.
// It is pure data with no dependency of its own, which is what makes this package
// the right home — same reason the rest of the wire vocabulary is here.

// HookMatcherSubject is what a trigger's matcher is tested against, and it is
// the fact that decides whether a matcher means anything for that trigger.
//
// The three values mirror KAS's own matcherSubjectForTrigger, spelling included,
// read off the stock 2.19.2 bundle. Keeping the spellings identical is
// deliberate: the classification is upstream's, so a divergence here would be
// vibekit inventing a rule and attributing it to KAS.
type HookMatcherSubject string

const (
	// HookMatcherSubjectToolName means the matcher is tested against the tool
	// NAME. A hook with no matcher runs on every tool call, which is legitimate
	// and worth saying out loud rather than refusing.
	HookMatcherSubjectToolName HookMatcherSubject = "toolName"
	// HookMatcherSubjectFilePath means the matcher is tested against the file
	// PATH. A matcher here is fully effective, so neither presence nor absence
	// is a defect.
	HookMatcherSubjectFilePath HookMatcherSubject = "filePath"
	// HookMatcherSubjectNone means the trigger carries nothing to match on, so a
	// matcher is IGNORED. Upstream warns to its own log and tells no client, which
	// is why vibekit refuses the pairing at creation instead.
	HookMatcherSubjectNone HookMatcherSubject = "none"
)

// HookTrigger is one canonical trigger name with its matcher subject.
type HookTrigger struct {
	// Name is the PascalCase trigger name KAS's hook loader expects.
	Name string
	// Subject is what this trigger's matcher is tested against.
	Subject HookMatcherSubject
}

// The eleven canonical trigger names, as KAS's hook loader spells them.
//
// Constants rather than repeated literals because the alias table below points
// many spellings at ONE canonical name, and that is the file's invariant: a typo
// in an alias would otherwise mint a silent twelfth trigger that KAS then drops.
// Unexported — nothing outside this package names a trigger, it normalizes one.
const (
	triggerSessionStart     = "SessionStart"
	triggerStop             = "Stop"
	triggerPreToolUse       = "PreToolUse"
	triggerPostToolUse      = "PostToolUse"
	triggerPreTaskExec      = "PreTaskExec"
	triggerPostTaskExec     = "PostTaskExec"
	triggerUserPromptSubmit = "UserPromptSubmit"
	triggerPostFileCreate   = "PostFileCreate"
	triggerPostFileSave     = "PostFileSave"
	triggerPostFileDelete   = "PostFileDelete"
	triggerManual           = "Manual"
)

// hookTriggers maps event-type payload values (vibekit's own vocabulary plus v2
// / Kiro-IDE camelCase aliases) to the canonical trigger. Keys are lowercased
// for case-insensitive lookup, and every alias points at the SAME meta as its
// canonical name, so an alias can never disagree about a subject.
//
// The set is CLOSED and the partition over subjects is MECE: all 11 canonical
// triggers appear exactly once and each has exactly one subject, which is what
// makes the pairing check below total. Adding a trigger means adding its subject
// in the same edit — TestEveryCanonicalTriggerHasASubject fails otherwise.
var hookTriggers = map[string]HookTrigger{
	// Canonical PascalCase names (self-map via their lowercase key).
	"sessionstart":     {Name: triggerSessionStart, Subject: HookMatcherSubjectNone},
	"stop":             {Name: triggerStop, Subject: HookMatcherSubjectNone},
	"pretooluse":       {Name: triggerPreToolUse, Subject: HookMatcherSubjectToolName},
	"posttooluse":      {Name: triggerPostToolUse, Subject: HookMatcherSubjectToolName},
	"pretaskexec":      {Name: triggerPreTaskExec, Subject: HookMatcherSubjectNone},
	"posttaskexec":     {Name: triggerPostTaskExec, Subject: HookMatcherSubjectNone},
	"userpromptsubmit": {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"postfilecreate":   {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"postfilesave":     {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"postfiledelete":   {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"manual":           {Name: triggerManual, Subject: HookMatcherSubjectNone},
	// v2 / Kiro-IDE camelCase aliases.
	"agentstop":         {Name: triggerStop, Subject: HookMatcherSubjectNone},
	"promptsubmit":      {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"userprompt":        {Name: triggerUserPromptSubmit, Subject: HookMatcherSubjectNone},
	"pretaskexecution":  {Name: triggerPreTaskExec, Subject: HookMatcherSubjectNone},
	"posttaskexecution": {Name: triggerPostTaskExec, Subject: HookMatcherSubjectNone},
	"filecreate":        {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"filecreated":       {Name: triggerPostFileCreate, Subject: HookMatcherSubjectFilePath},
	"filesave":          {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"filesaved":         {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"fileedit":          {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"fileedited":        {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
	"filedelete":        {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"filedeleted":       {Name: triggerPostFileDelete, Subject: HookMatcherSubjectFilePath},
	"usertriggered":     {Name: triggerManual, Subject: HookMatcherSubjectNone},
	// Three more spellings KAS itself accepts. Their absence meant a payload
	// using any of them produced a hook file KAS then discarded.
	"agentspawn":    {Name: triggerSessionStart, Subject: HookMatcherSubjectNone},
	"sessionend":    {Name: triggerStop, Subject: HookMatcherSubjectNone},
	"afterfileedit": {Name: triggerPostFileSave, Subject: HookMatcherSubjectFilePath},
}

// NormalizeHookTrigger maps a client event-type value (or a canonical name KAS
// reported back) to its canonical trigger, reporting whether the value is one KAS
// will actually load.
//
// It used to pass an unrecognised value through trimmed, on the reasoning that
// vibekit should not block a trigger its map does not yet know. That reasoning
// inverts here, because the permissive branch is not lenient, it is silent:
// KAS's parseHookDocument DROPS a hook whose trigger it does not recognise, so
// create_hook answered 200 with a file path for a hook that loads nowhere, never
// fires, and never appears in /api/hooks. The user is told a hook exists and
// there is no signal anywhere that it does not.
//
// Refusing costs nothing by comparison: the closed set lives in this map, and a
// trigger KAS adds later is one map entry away. Silence was the expensive choice.
func NormalizeHookTrigger(eventType string) (HookTrigger, bool) {
	t, ok := hookTriggers[strings.ToLower(strings.TrimSpace(eventType))]
	return t, ok
}

// KnownHookTriggers lists the canonical trigger names for an error message, so a
// rejection tells the caller what IS accepted rather than only what is not.
func KnownHookTriggers() string {
	seen := make(map[string]struct{}, len(hookTriggers))
	names := make([]string, 0, len(hookTriggers))
	for _, v := range hookTriggers {
		if _, dup := seen[v.Name]; dup {
			continue
		}
		seen[v.Name] = struct{}{}
		names = append(names, v.Name)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// HookMatcherDefect is what is wrong with a trigger-and-matcher pairing, and the
// two members are deliberately on DIFFERENT surfaces because they are different
// kinds of mistake.
type HookMatcherDefect string

const (
	// HookMatcherOK means the pairing is fine.
	HookMatcherOK HookMatcherDefect = ""
	// HookMatcherIneffective means a matcher was supplied for a trigger that has
	// nothing to match on, so it is IGNORED. Always a mistake, cheap to fix at
	// the form, and therefore refused at creation.
	HookMatcherIneffective HookMatcherDefect = "ineffective"
	// HookMatcherMissingToolName means a tool-name trigger carries no matcher, so
	// the hook runs on EVERY tool call. A legitimate choice ("run on every tool
	// call") that must not be blocked, so it is a badge on the read surface.
	HookMatcherMissingToolName HookMatcherDefect = "missing_tool_matcher"
)

// ClassifyHookMatcher reports what is wrong with a trigger-and-matcher pairing,
// and it is ONE function so the write side and the read side cannot disagree.
//
// Both defects mirror a diagnostic KAS already computes and then keeps to
// itself: reportMatcherDiagnostics logs a warning and counts
// hooks.ineffectiveMatcher / hooks.missingToolMatcher into its own telemetry,
// with nothing on the wire, so a client is told nothing either way. Both
// conditions are one comparison away from data vibekit already holds, which is
// why they are computed here rather than requested upstream.
//
// An unknown trigger returns HookMatcherOK, deliberately: it is a different and
// larger defect, and the boundary that can refuse it (NormalizeHookTrigger's
// second return) has already done so. Reporting a matcher complaint about a
// trigger that will never load would name the wrong problem.
func ClassifyHookMatcher(trigger, matcher string) HookMatcherDefect {
	t, ok := NormalizeHookTrigger(trigger)
	if !ok {
		return HookMatcherOK
	}
	hasMatcher := strings.TrimSpace(matcher) != ""
	switch t.Subject {
	case HookMatcherSubjectToolName:
		if !hasMatcher {
			return HookMatcherMissingToolName
		}
	case HookMatcherSubjectNone:
		if hasMatcher {
			return HookMatcherIneffective
		}
	case HookMatcherSubjectFilePath:
		// A path matcher is fully effective and its absence is a legitimate
		// "every file". Nothing to report in either direction, and saying so is
		// the point: this arm is what keeps the switch exhaustive over the
		// subject enum rather than leaving filePath in a default nobody reads.
	}
	return HookMatcherOK
}
