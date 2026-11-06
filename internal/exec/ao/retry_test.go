package ao

import "testing"

func TestTransientProviderErrors(t *testing.T) {
	for _, text := range []string{"HTTP 429", "overloaded", "ECONNRESET", "transport error", "temporarily unavailable"} {
		if !transientProviderError(text) {
			t.Fatalf("%q not transient", text)
		}
	}
	for _, text := range []string{"invalid JSON", "permission denied", "unknown session", "review scope", "no reply within 5m0s"} {
		if transientProviderError(text) {
			t.Fatalf("%q incorrectly transient", text)
		}
	}
}
