package httpreply

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMethodNotAllowedSetsAllow pins the RFC 9110 §15.5.6 requirement that a
// 405 names the resource's permitted methods, and the §10.2.1 rendering of
// that list. Every 405 vibekit emits routes through MethodNotAllowed, so this
// is the one place the header format is decided.
func TestMethodNotAllowedSetsAllow(t *testing.T) {
	tests := map[string]struct {
		call func(http.ResponseWriter)
		want string
	}{
		"single method": {
			call: func(w http.ResponseWriter) { MethodNotAllowed(w, http.MethodPost) },
			want: "POST",
		},
		"two methods keep call order": {
			call: func(w http.ResponseWriter) { MethodNotAllowed(w, http.MethodGet, http.MethodPut) },
			want: "GET, PUT",
		},
		"four methods": {
			call: func(w http.ResponseWriter) {
				MethodNotAllowed(w, http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete)
			},
			want: "GET, PUT, PATCH, DELETE",
		},
		// RFC 9110 §5.6.1.1 forbids generating empty list elements.
		"empty variadic entries are dropped": {
			call: func(w http.ResponseWriter) { MethodNotAllowed(w, http.MethodGet, "", http.MethodPost, "") },
			want: "GET, POST",
		},
		// RFC 9110 §9.1: method tokens are case-sensitive, so a caller's
		// spelling is emitted verbatim rather than normalised.
		"tokens are emitted verbatim": {
			call: func(w http.ResponseWriter) { MethodNotAllowed(w, "get") },
			want: "get",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec)

			if got := rec.Header().Get("Allow"); got != tc.want {
				t.Errorf("Allow = %q, want %q", got, tc.want)
			}
			// The header is the whole change: status and body must be
			// exactly what MethodNotAllowed emitted before it existed.
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"method not allowed"}` {
				t.Errorf("body = %q, want the unchanged bare error envelope", got)
			}
		})
	}
}

// TestRequireMethodSetsAllowOnlyWhenItRejects covers the guard that most
// callers reach MethodNotAllowed through: a rejection must advertise the
// single permitted method, and a match must not touch the header at all
// (Allow on a 2xx would claim the response is a method-list carrier).
func TestRequireMethodSetsAllowOnlyWhenItRejects(t *testing.T) {
	t.Run("rejection sets Allow", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)

		if RequireMethod(rec, req, http.MethodPost) {
			t.Fatal("RequireMethod allowed GET against a POST-only resource")
		}
		if got := rec.Header().Get("Allow"); got != "POST" {
			t.Errorf("Allow = %q, want %q", got, "POST")
		}
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("match leaves Allow unset", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", nil)

		if !RequireMethod(rec, req, http.MethodPost) {
			t.Fatal("RequireMethod rejected the permitted method")
		}
		if got := rec.Header().Get("Allow"); got != "" {
			t.Errorf("Allow = %q on an accepted request, want unset", got)
		}
	})
}
