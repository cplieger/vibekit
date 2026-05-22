// File-system request handlers for kiro-cli ACP bridges.
//
// kiro-cli can ask us to read or write files within the workspace via
// the fs/read_text_file and fs/write_text_file methods (both require the
// matching clientCapability flag). Routing through us rather than letting
// kiro-cli open files directly means the agent respects the same
// workspace scoping the user's file browser uses: every path is resolved
// against WORKDIR and symlinks/`..` traversal outside WORKDIR is rejected.
//
// Spec: https://agentclientprotocol.com/protocol/file-system
//
// Threading: translateACPEvent runs on the bridge's forward goroutine.
// fs handlers dispatch into their own goroutine so the forward loop
// keeps pumping session/update chunks while disk I/O is in flight.
// The bridge's Respond path is write-serialized by b.mu internally, so
// out-of-order responses for concurrent fs reads don't corrupt the
// stdin stream.
//
// Layout:
//   - bridge_fs_read.go:  handleFSRequest dispatch, respondFSRead, sliceByLines
//   - bridge_fs_write.go: respondFSWrite, chatInSupervisedMode, currentMessageCount
//   - bridge_fs_path.go:  resolveInsideWorkDir, respondFSError, fsErrorIsRoutine, respondBridge, constants
//   - bridge_fs_staging.go: stageFSWrite, extractToolCallID, readStagedOld, truncateForStaging

package hub
