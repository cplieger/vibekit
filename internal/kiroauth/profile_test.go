package kiroauth

import (
	"os"
	"path/filepath"
	"testing"
)

const testArn = "arn:aws:codewhisperer:us-east-1:713669222412:profile/7KHC74QYC9PQ"

// blob wraps a key/value pair the way sqlite stores a state row: the
// value bytes immediately follow the key bytes (record payload layout),
// surrounded by arbitrary page bytes.
func blob(parts ...string) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, []byte(p)...)
	}
	return b
}

func TestScanProfileArn(t *testing.T) {
	valid := `{"arn":"` + testArn + `","profile_name":"KiroProfile-us-east-1"}`
	tests := []struct {
		name    string
		data    []byte
		want    string
		wantErr bool
	}{
		{
			name: "AdjacentValue",
			data: blob("\x00\x01pageheader", "api.codewhisperer.profile", valid, "\x00trailing"),
			want: testArn,
		},
		{
			name: "KeyWithoutAdjacentObjectIsSkipped",
			// A non-value occurrence (e.g. an index entry): key not
			// immediately followed by '{'. Then a real value copy later.
			data: blob("api.codewhisperer.profile\x00\x00somegarbage no brace here", "morepad",
				"\x00api.codewhisperer.profile", valid),
			want: testArn,
		},
		{
			name: "MultipleAgreeingCopies",
			data: blob("api.codewhisperer.profile", valid, "pad",
				"api.codewhisperer.profile", valid),
			want: testArn,
		},
		{
			name: "FirstValidCopyWinsOverLaterGarbage",
			data: blob("api.codewhisperer.profile", valid, "pad",
				`api.codewhisperer.profile{"arn":"not-an-arn"}`),
			want: testArn,
		},
		{
			name:    "NonCodewhispererArnRejected",
			data:    blob(`api.codewhisperer.profile{"arn":"arn:aws:iam::123:user/x"}`),
			wantErr: true,
		},
		{
			name:    "MissingKey",
			data:    blob("no relevant key here", "{}"),
			wantErr: true,
		},
		{
			name:    "KeyAtEndNoValue",
			data:    blob("padding", "api.codewhisperer.profile"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scanProfileArn(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("scanProfileArn = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("scanProfileArn error: %v", err)
			}
			if got != tt.want {
				t.Errorf("scanProfileArn = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"Simple", `{"a":1}rest`, `{"a":1}`, true},
		{"Nested", `{"a":{"b":2}}x`, `{"a":{"b":2}}`, true},
		{"BraceInsideString", `{"a":"}"}tail`, `{"a":"}"}`, true},
		{"EscapedQuoteInString", `{"a":"x\"}y"}z`, `{"a":"x\"}y"}`, true},
		{"NotAnObject", `["a"]`, "", false},
		{"Unterminated", `{"a":1`, "", false},
		{"Empty", ``, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractJSONObject([]byte(tt.in))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProfileReaderArn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.sqlite3")
	content := blob("\x00sqliteheader", "api.codewhisperer.profile",
		`{"arn":"`+testArn+`","profile_name":"X"}`, "\x00")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewProfileReader(path)
	got, err := r.Arn()
	if err != nil {
		t.Fatalf("Arn: %v", err)
	}
	if got != testArn {
		t.Fatalf("Arn = %q, want %q", got, testArn)
	}

	// Second read hits the modtime cache and returns the same value.
	got2, err := r.Arn()
	if err != nil || got2 != testArn {
		t.Fatalf("cached Arn = %q, err %v", got2, err)
	}
	if !r.loaded {
		t.Error("reader should be marked loaded after a successful read")
	}
}

func TestProfileReaderMissingFile(t *testing.T) {
	r := NewProfileReader(filepath.Join(t.TempDir(), "nope.sqlite3"))
	if _, err := r.Arn(); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDefaultProfileDBPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got := DefaultProfileDBPath(); got != "/xdg/data/kiro-cli/data.sqlite3" {
		t.Errorf("DefaultProfileDBPath = %q", got)
	}
}
