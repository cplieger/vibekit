package auth

import "testing"

func FuzzValidateProvider(f *testing.F) {
	f.Add("")
	f.Add("https://example.com")
	f.Add("https://login.aws.amazon.com/")
	f.Add("http://not-https.com")
	f.Add("https://user:[email protected]")
	f.Add("not a url at all")
	f.Add("ftp://wrong.scheme.com")
	f.Add("https://" + string(make([]byte, 3000)))

	f.Fuzz(func(t *testing.T, v string) {
		err := validateProvider(v)
		if v == "" && err != nil {
			t.Error("empty provider must be valid")
		}
		if err == nil && v != "" && len(v) > maxProviderLen {
			t.Error("should reject providers exceeding maxProviderLen")
		}
	})
}

func FuzzValidateRegion(f *testing.F) {
	f.Add("")
	f.Add("us-east-1")
	f.Add("eu-west-2")
	f.Add("cn-north-1")
	f.Add("us-gov-west-1")
	f.Add("us-iso-east-1")
	f.Add("invalid")
	f.Add("--help")
	f.Add("us--east-1")
	f.Add("US-EAST-1")

	f.Fuzz(func(t *testing.T, v string) {
		err := validateRegion(v)
		if v == "" && err != nil {
			t.Error("empty region must be valid")
		}
		if err == nil && v != "" && len(v) > maxRegionLen {
			t.Error("should reject regions exceeding maxRegionLen")
		}
	})
}
