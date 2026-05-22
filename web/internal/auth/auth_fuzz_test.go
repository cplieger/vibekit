package auth

import "testing"

func FuzzExtractAuthURL(f *testing.F) {
	f.Add("Open this URL: https://example.com/auth?code=abc")
	f.Add("https://login.example.com/device")
	f.Add("Open this URL:")
	f.Add("")
	f.Add("no url here at all")
	f.Add("http://not-https.example.com")

	f.Fuzz(func(t *testing.T, line string) {
		result := extractAuthURL(line)
		if result != "" && len(result) < len("https://") {
			t.Errorf("returned non-empty result shorter than https:// prefix: %q", result)
		}
		if result != "" && result[:8] != "https://" {
			t.Errorf("returned URL without https:// prefix: %q", result)
		}
	})
}
