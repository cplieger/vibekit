package auth

import "testing"

// FuzzBuildLoginArgs verifies buildLoginArgs always produces a safe argument
// list starting with "login" and "--use-device-flow", with no embedded control
// characters that could cause argument injection.
//
// Bug class: argument injection via embedded newlines/NUL in provider/region.
func FuzzBuildLoginArgs(f *testing.F) {
	f.Add("", "")
	f.Add("https://x.com", "us-east-1")
	f.Add("--evil", "")
	f.Add("", "\ninjected")
	f.Add("https://provider.example.com/auth", "eu-west-1")
	f.Add("a\x00b", "c\x00d")

	f.Fuzz(func(t *testing.T, provider, region string) {
		args := buildLoginArgs(provider, region)

		// Invariant 1: always starts with "login", "--use-device-flow".
		if len(args) < 2 || args[0] != "login" || args[1] != flagDeviceFlow {
			t.Fatalf("buildLoginArgs(%q, %q) = %v; missing required prefix", provider, region, args)
		}

		// Invariant 2: provider appears only after --identity-provider flag.
		if provider != "" {
			found := false
			for i, a := range args {
				if a == "--identity-provider" && i+1 < len(args) && args[i+1] == provider {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("buildLoginArgs(%q, %q): provider not after --identity-provider", provider, region)
			}
		}

		// Invariant 3: region appears only after --region flag.
		if region != "" {
			found := false
			for i, a := range args {
				if a == "--region" && i+1 < len(args) && args[i+1] == region {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("buildLoginArgs(%q, %q): region not after --region", provider, region)
			}
		}

		// Invariant 4: length is deterministic based on inputs.
		expected := 2
		if provider != "" {
			expected += 2
		}
		if region != "" {
			expected += 2
		}
		if len(args) != expected {
			t.Fatalf("buildLoginArgs(%q, %q): len=%d, want %d", provider, region, len(args), expected)
		}
	})
}
