// File-system request handlers for kiro-cli ACP bridges.
//
// kiro-cli asks us to read or write workspace files via fs/read_text_file and
// fs/write_text_file, so the agent respects the same workspace scoping the
// file browser uses: every path is resolved against WORKDIR and symlink/`..`
// traversal outside it is rejected.
//
// Spec: https://agentclientprotocol.com/protocol/file-system
//
// Threading: fs handlers dispatch into their own goroutine so the forward
// loop keeps pumping session/update chunks while disk I/O is in flight.
//
// Layout: bridge_fs_read.go (reads), bridge_fs_write.go (writes),
// bridge_fs_path.go (confinement + error helpers).
//
// There is no staging file. KAS gates a whole turn (autopilot: false), so a
// write reaching here is already authorized — see bridge_fs_write.go.

package agent
