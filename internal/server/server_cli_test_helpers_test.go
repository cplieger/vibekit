// Test helpers for server_cli_test.go. Lives in a _test.go file so
// the compiler only builds it during `go test`, keeping deadcode
// happy (production code uses safeKiroSettingValueFor directly with
// an explicit kind; this convenience wrapper picks a kind by trying
// both).
//
// If production code ever needs this helper, move it back to
// server_cli.go. For now, the only call sites are server_test.go.

package server

// safeKiroSettingValue is a convenience wrapper around
// safeKiroSettingValueFor that tries both settingBool and settingInt
// and returns the first non-empty result. Used by tests that want to
// exercise "try-both" behavior without specifying a kind.
func safeKiroSettingValue(v string) string {
	if r := safeKiroSettingValueFor(v, settingBool); r != "" {
		return r
	}
	return safeKiroSettingValueFor(v, settingInt)
}
