package vibekit

// EffectiveSettings is what GET /api/settings answers: the value in force for
// every vibekit-owned preference the client renders, resolved against the stored
// config.json rather than echoed from it.
//
// NO FIELD CARRIES omitempty, and that is the contract rather than a style
// choice. wiregen emits a REQUIRED TypeScript field for a Go field without
// omitempty and an optional one for a field with it, so this struct generates an
// interface whose every member is present. That is what lets the client delete
// its own copies of these defaults: a reader cannot supply a fallback for a field
// the type says is always there, so the drift class stops being representable
// instead of being tested for.
//
// It is a STRUCT rather than the map the handler used to emit, for two reasons.
// A struct cannot omit a field, so completeness is a property of the type instead
// of a property of whichever builder ran; and a struct is what wiregen can carry
// into TypeScript, which is what stops the client's copy being hand-maintained.
// The cost is that an unknown key in config.json does not reach the client — see
// the handler for why that loses nothing.
//
// The PATCH body is the PARTIAL of this type. Full shape to read, partial to
// write, which is exactly what the two verbs mean.
//
// FIELD ORDER IS ALIGNMENT-DRIVEN, not narrative: the strings precede the slice
// (a slice's pointer is its first word, its len and cap are not) and both precede
// every non-pointer field, which is what govet's fieldalignment demands. Grouping
// these by topic instead costs 16 bytes of GC scan prefix and fails the gate.
type EffectiveSettings struct {
	// Theme is "", "dark", "light" or "system". The empty string is a REAL value
	// meaning nothing has been chosen, which the client resolves to the OS
	// preference; it is deliberately not normalised to "system" here, because the
	// client's one-time paint-cache carry-across is reachable only while the
	// server says nothing.
	Theme string `json:"theme"`
	// FBPath is the file-browser path to restore, "" to list the granted mounts.
	FBPath string `json:"fb_path"`
	// LastModel and LastEffort are what a NEW chat opens on. Both are pure memory:
	// the value in force for an existing chat lives on that chat's record.
	LastModel  string `json:"last_model"`
	LastEffort string `json:"last_effort"`
	// LastEffortModel is the model LastEffort was picked under; the seed applies
	// only to a chat running that model (settings.KeyLastEffortModel).
	LastEffortModel string `json:"last_effort_model"`
	// AgentIgnoreFiles is the ignore-file basename list the agent read filter
	// applies. Its default is non-empty (settings.DefaultAgentIgnoreFiles), which
	// is why an absent key must not read as the zero value: the client rendered an
	// empty chip row while the filter was applying two patterns, and the first
	// edit persisted that emptiness.
	AgentIgnoreFiles []string `json:"agent_ignore_files"`
	// ChatRetentionDays is -1 (keep forever), 0 (delete on close) or a day count.
	// Zero is the most destructive value in the document, so absent must never
	// resolve to it.
	ChatRetentionDays int `json:"chat_retention_days"`
	// KnowledgeEnabled defaults TRUE, so it is the other key whose zero value is
	// the wrong answer: the index, its REST surface and its UI all predate the
	// switch, so an absent key read as false takes the knowledge tool away from
	// every existing install.
	KnowledgeEnabled bool `json:"knowledge_enabled"`
	// ToolSearchEnabled and MemoryEnabled both default off, matching kiro-cli and
	// the standing memory veto respectively.
	ToolSearchEnabled bool `json:"tool_search_enabled"`
	MemoryEnabled     bool `json:"memory_enabled"`
	// NotificationsEnabled is the push master switch, default off. The two per-kind
	// switches below default ON, mirroring push.kindRegistry — the polarity differs
	// between the master and the kinds on purpose, and that asymmetry is exactly
	// why the client must not guess either of them.
	NotificationsEnabled bool `json:"notifications_enabled"`
	NotifyAgentFinished  bool `json:"notify_agent_finished"`
	NotifyPRStatus       bool `json:"notify_pr_status"`
	// SupervisedDefault seeds newly created chats; ScheduledAutoApprove decides an
	// unattended run's permission ask at its deadline and is fail-closed by
	// decision. DebugLogs raises the log level. All three default off.
	SupervisedDefault    bool `json:"supervised_default"`
	ScheduledAutoApprove bool `json:"scheduled_auto_approve"`
	DebugLogs            bool `json:"debug_logs"`
}
