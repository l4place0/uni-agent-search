package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// Brave rejects q longer than 400 characters with 422 VALIDATION. The Brave
// branch cuts the tail instead, and the cut must stay valid UTF-8 and be
// visible on the attempt and in the log rather than silently changing results.
func TestBraveQueryIsTruncatedByRuneAndReported(t *testing.T) {
	a := testApp(t)
	var (
		mu  sync.Mutex
		got string
	)
	seen := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got, seen = r.URL.Query().Get("q"), true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"results":[{"title":"Go docs","url":"https://go.dev/doc/","description":"Official"}]}}`)
	}))
	defer up.Close()
	if _, e := a.db.Exec(`UPDATE providers SET enabled=0`); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec(`UPDATE providers SET enabled=1, base_url=? WHERE id='brave'`, up.URL); e != nil {
		t.Fatal(e)
	}
	if _, e := a.addKey("brave", "test", "test-brave-secret", 1, 2); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	query := strings.Repeat("中", 2000)
	body, _ := json.Marshal(SearchInput{Query: query, Providers: []string{"brave"}, NoCache: true})
	w := call(a, http.MethodPost, "/v1/search", a.clientToken, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out SearchOutput
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}

	mu.Lock()
	sent, captured := got, seen
	mu.Unlock()
	if !captured {
		t.Fatal("Brave was never called")
	}
	if !utf8.ValidString(sent) {
		t.Fatal("truncated query is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(sent); n != braveQueryLimit {
		t.Fatalf("sent %d characters, want %d", n, braveQueryLimit)
	}
	if want := string([]rune(query)[:braveQueryLimit]); sent != want {
		t.Fatal("truncation kept the wrong part of the query")
	}
	if len(out.Attempts) != 1 || !out.Attempts[0].OK {
		t.Fatalf("unexpected attempts: %+v", out.Attempts)
	}
	if d := out.Attempts[0].Degraded; len(d) != 1 || d[0] != "q truncated 2000->400" {
		t.Fatalf("attempt degradation marker: %+v", d)
	}
	if !strings.Contains(logs.String(), "q truncated 2000->400") {
		t.Fatalf("log line missing: %s", logs.String())
	}
}

// Only Brave has the 400-character cap; every other provider still receives
// the whole query.
func TestNonBraveProviderKeepsFullQuery(t *testing.T) {
	a := testApp(t)
	var (
		mu  sync.Mutex
		got string
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.URL.Query().Get("q")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss><channel><item><title>Go docs</title><link>https://go.dev/doc/</link><description>Official</description></item></channel></rss>`)
	}))
	defer up.Close()
	if _, e := a.db.Exec(`UPDATE providers SET enabled=0; UPDATE providers SET enabled=1, base_url=? WHERE id='bing'`, up.URL); e != nil {
		t.Fatal(e)
	}
	query := strings.Repeat("中", 2000)
	body, _ := json.Marshal(SearchInput{Query: query, Providers: []string{"bing"}, NoCache: true})
	w := call(a, http.MethodPost, "/v1/search", a.clientToken, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	mu.Lock()
	sent := got
	mu.Unlock()
	if n := utf8.RuneCountInString(sent); n != 2000 {
		t.Fatalf("bing received %d characters, want all 2000", n)
	}
	if sent != query {
		t.Fatal("bing query was modified")
	}
	var out SearchOutput
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if len(out.Attempts) != 1 || len(out.Attempts[0].Degraded) != 0 {
		t.Fatalf("unexpected degradation on bing: %+v", out.Attempts)
	}
}
