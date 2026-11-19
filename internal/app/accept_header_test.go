package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Brave's gateway validates Accept against an enum ("application/json" or
// "*/*") and answers everything else with 422 VALIDATION before the request
// reaches search. This test pins the exact Brave value and proves that the
// shared, broader Accept of every other provider did not change with it.
func TestProviderAcceptHeaderContract(t *testing.T) {
	var (
		mu  sync.Mutex
		got []string
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Values("Accept")...)
		mu.Unlock()
		// The response never matters here: only the request headers do.
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer up.Close()

	for _, tc := range []struct{ kind, want string }{
		{"brave", "application/json"},
		{"bing", defaultAccept},
		{"duckduckgo", defaultAccept},
		{"tavily", defaultAccept},
		{"exa", defaultAccept},
		{"zhihu", defaultAccept},
		{"doubao", defaultAccept},
		{"xai", "application/json"},
		{"grok-compatible", "application/json"},
		{"searxng", "application/json"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			mu.Lock()
			got = nil
			mu.Unlock()
			a := testApp(t)
			p := Provider{ID: tc.kind, Kind: tc.kind, BaseURL: up.URL, Timeout: 2, Model: "test-model"}
			_, _ = a.requestProvider(context.Background(), p, "test-key", "go", 8)
			mu.Lock()
			seen := append([]string(nil), got...)
			mu.Unlock()
			if len(seen) != 1 || seen[0] != tc.want {
				t.Fatalf("%s sent Accept %q, want exactly %q", tc.kind, seen, tc.want)
			}
		})
	}
}

// Brave must never receive the broad Accept, not even as one value of a list:
// the gateway compares the whole header against its enum, so the table above
// pins the single literal value and fails on any extra member.
