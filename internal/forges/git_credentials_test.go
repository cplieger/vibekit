package forges

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScrubGitCredentials covers the legacy cleartext-store scrub:
// lines for the host go (both with and without a trailing slash),
// unrelated lines stay, an emptied file is removed, a missing file is
// a no-op.
func TestScrubGitCredentials(t *testing.T) {
	cases := []struct {
		name     string
		seed     string // "" = no file
		host     string
		want     string // expected content after scrub; "" = file absent
		wantGone bool
	}{
		{
			name: "removes host lines, keeps others",
			seed: "https://oauth2:tok@gitea.example.com/\nhttps://u:t@other.example/\n",
			host: "gitea.example.com",
			want: "https://u:t@other.example/\n",
		},
		{
			name: "matches authority without trailing slash",
			seed: "https://oauth2:tok@gitea.example.com\nhttps://u:t@other.example/\n",
			host: "gitea.example.com",
			want: "https://u:t@other.example/\n",
		},
		{
			name:     "removes the file when the last line goes",
			seed:     "https://oauth2:tok@gitea.example.com/\n",
			host:     "gitea.example.com",
			wantGone: true,
		},
		{
			name: "no matching line leaves the file byte-identical",
			seed: "https://u:t@other.example/\n",
			host: "gitea.example.com",
			want: "https://u:t@other.example/\n",
		},
		{
			name:     "missing file is a no-op",
			seed:     "",
			host:     "gitea.example.com",
			wantGone: true,
		},
		{
			name: "host that is a suffix of another host does not match it",
			seed: "https://u:t@notgitea.example.com/\n",
			host: "gitea.example.com",
			want: "https://u:t@notgitea.example.com/\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			credFile := filepath.Join(home, ".git-credentials")
			if tc.seed != "" {
				if err := os.WriteFile(credFile, []byte(tc.seed), 0o600); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			if err := scrubGitCredentials(tc.host); err != nil {
				t.Fatalf("scrubGitCredentials: %v", err)
			}
			data, err := os.ReadFile(credFile)
			if tc.wantGone {
				if err == nil {
					t.Errorf("file should be gone, has %q", data)
				}
				return
			}
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("content = %q, want %q", data, tc.want)
			}
		})
	}
}
