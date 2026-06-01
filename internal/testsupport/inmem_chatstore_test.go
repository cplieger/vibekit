package testsupport

import (
	"testing"

	"vibekit/internal/api"
)

func TestInMemoryChatStore_Contract(t *testing.T) {
	ChatStoreContractTest(t, func(t *testing.T) api.ChatStore {
		t.Helper()
		return NewInMemoryChatStore()
	})
}
