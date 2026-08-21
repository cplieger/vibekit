package testsupport

import (
	"testing"
)

func TestInMemoryChatStore_Contract(t *testing.T) {
	ChatStoreContractTest(t, func(t *testing.T) ChatStoreContract {
		t.Helper()
		return NewInMemoryChatStore()
	})
}
