package chat

import (
	"testing"

	"vibekit/internal/api"
	"vibekit/internal/testsupport"
)

func TestStore_ChatStoreContract(t *testing.T) {
	testsupport.ChatStoreContractTest(t, func(t *testing.T) api.ChatStore {
		t.Helper()
		s, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		return s
	})
}
