package textsearch

// Tally is what a scan reports beside its matches. Embedded, so each reply
// spells the three facts once and wiregen flattens them into its TS type.
type Tally struct {
	// Scanned is how many units the scan READ, whole or in part: chats, files,
	// messages; or whose index entry, built from one such read, stood in for it.
	// A unit it read and found out of scope (a binary) is scanned. A unit whose
	// name no longer held what the walk classified (vanished, swapped, not a
	// regular file) is a skip the answer covers, and stays scanned. A unit it
	// meant to read and could not (a chat over chatFileCap, a permission or I/O
	// error on the read) is not scanned, and Truncated is what says so.
	Scanned int `json:"scanned"`
	// Matched is how many rows Matches would hold had nothing cut it: the same
	// unit as Matches (hits, matching lines, chats). The list is cut iff
	// Matched > len(Matches). Truncated never means a cut.
	Matched int `json:"matched"`
	// Truncated is true when the scan did not read everything it was asked to:
	// a cap on files or chats, a file read partially or not at all, a dead context.
	Truncated bool `json:"truncated"`
}
