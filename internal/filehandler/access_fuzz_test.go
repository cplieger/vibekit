package filehandler

import "testing"

func FuzzEnforceAccess(f *testing.F) {
	f.Add("/workspace/file.txt")
	f.Add("/etc/passwd")
	f.Add("/config/chats/a.json")
	f.Add("/config/kiro/steering/vibekit.md")
	f.Add("/../etc/shadow")
	f.Add("/app/../workspace")
	f.Add("/\x00etc")
	f.Add("/CONFIG/CHATS/x")

	f.Fuzz(func(t *testing.T, path string) {
		_ = enforceAccess(path)
	})
}

func FuzzIsProtectedDir(f *testing.F) {
	f.Add("/config")
	f.Add("/config/chats")
	f.Add("/config/chats/")
	f.Add("/workspace")
	f.Add("/config/kiro/agents")
	f.Add("/")

	f.Fuzz(func(t *testing.T, path string) {
		_ = isProtectedDir(path)
	})
}
