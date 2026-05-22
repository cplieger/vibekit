package main

import "os"

// envOr returns the environment variable value or the fallback if unset/empty.
// Moved to test file because it's only exercised by main_test.go; production
// code uses composition.ConfigFromEnv which has its own envOr.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
