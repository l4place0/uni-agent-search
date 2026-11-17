package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// zhihuMock answers the search and quota endpoints. The handler decides the
// business code per test case; every request is asserted to carry the Bearer
// token and a fresh timestamp.
func zhihuMock(t *testing.T, searchCode int, quotaRemaining int64, items string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-zhihu-secret" {
			t.Errorf("unexpected authorization header")
		}
		ts, err := strconv.ParseInt(r.Header.Get("X-Request-Timestamp"), 10, 64)
		if err != nil || time.Since(time.Unix(ts, 0)) > 10*time.Minute {
			t.Errorf("missing or stale X-Request-Timestamp: %q", r.Header.Get("X-Request-Timestamp"))
		}
		switch r.URL.Path {
		case "/api/v1/content/zhihu_search":
			if r.Method != http.MethodGet || r.URL.Query().Get("Query") == "" {
				t.Errorf("unexpected zhihu request: %s %s", r.Method, r.URL)
			}
			if n, e := strconv.Atoi(r.URL.Query().Get("Count")); e != nil || n > 10 || n < 1 {
				t.Errorf("Count out of range: %q", r.URL.Query().Get("Count"))
			}
			w.Header().Set("Content-Type", "application/json")
			if searchCode != 0 {
				fmt.Fprintf(w, `{"Code":%d,"Message":"rejected","Data":null}`, searchCode)
				return
			}
			fmt.Fprintf(w, `{"Code":0,"Message":"success","Data":{"HasMore":false,"Items":[%s]}}`, items)
		case "/api/v1/quota":
			if r.URL.Query().Get("APIIDs") != "zhihu_search" {
				t.Errorf("quota queried without APIIDs: %s", r.URL)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Code":0,"Message":"success","Data":[{"APIID":"zhihu_search","APIName":"知乎搜索","TotalQuota":5000,"TotalUsed":%d,"RemainingQuota":%d}]}`, 5000-quotaRemaining, quotaRemaining)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
}

func doubaoMock(t *testing.T, codeN int, code string, webResults string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search_api/web_search" || r.Method != http.MethodPost {
			t.Errorf("unexpected doubao request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-doubao-secret" {
			t.Errorf("unexpected authorization header")
		}
		var in struct {
			Query        string
			SearchType   string
			Count        int
			Filter       struct{ NeedUrl bool }
			QueryControl struct{ QueryRewrite bool }
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("invalid doubao body: %v", err)
		}
		if in.Query == "" || in.SearchType != "web" || in.Count < 1 || in.Count > 50 || !in.Filter.NeedUrl {
			t.Errorf("unexpected doubao body: %+v", in)
		}
		w.Header().Set("Content-Type", "application/json")
		if codeN != 0 || code != "" {
			fmt.Fprintf(w, `{"ResponseMetadata":{"RequestId":"r1","Error":{"CodeN":%d,"Code":%q,"Message":"rejected"}},"Result":null}`, codeN, code)
			return
		}
		fmt.Fprintf(w, `{"ResponseMetadata":{"RequestId":"r1"},"Result":{"ResultCount":1,"WebResults":[%s]}}`, webResults)
	}))
}

func TestZhihuProviderSearchQuotaAndAuth(t *testing.T) {
	const item = `{"Title":"RAG 评测方法综述","ContentText":"本文介绍了主流 RAG 评测框架","Url":"https://zhuanlan.zhihu.com/p/123456789?utm_medium=openapi_platform","AuthorName":"张三","CreateTime":1710000000}`
	a := testApp(t)
	up := zhihuMock(t, 0, 5000, item)
	defer up.Close()
	if _, err := a.db.Exec(`UPDATE providers SET enabled=1,base_url=? WHERE id='zhihu'`, up.URL); err != nil {
		t.Fatal(err)
	}
	id, err := a.addKey("zhihu", "main", "test-zhihu-secret", 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	w := call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"RAG 评测","providers":["zhihu"],"max_results":5,"no_cache":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out SearchOutput
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].URL != "https://zhuanlan.zhihu.com/p/123456789" || out.Results[0].Author != "张三" {
		t.Fatalf("unexpected results: %+v", out.Results)
	}
	if len(out.Results[0].Providers) != 1 || out.Results[0].Providers[0] != "zhihu" || out.Results[0].Ranks["zhihu"] != 1 {
		t.Fatalf("result not attributed to the provider: %+v", out.Results[0])
	}
	if len(out.Attempts) != 1 || !out.Attempts[0].OK || out.Attempts[0].KeyID != id {
		t.Fatalf("unexpected attempts: %+v", out.Attempts)
	}

	// Auth failure arrives as HTTP 200 with Code 20001: the key must be disabled.
	up2 := zhihuMock(t, 20001, 0, "")
	defer up2.Close()
	if _, err := a.db.Exec(`UPDATE providers SET base_url=? WHERE id='zhihu'`, up2.URL); err != nil {
		t.Fatal(err)
	}
	a.invalidate()
	w = call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["zhihu"],"max_results":3,"no_cache":true}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body)
	}
	var failed SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &failed)
	if len(failed.Attempts) == 0 || !strings.Contains(failed.Attempts[0].Error, "auth rejected") {
		t.Fatalf("unexpected attempt: %+v", failed.Attempts)
	}
	var enabled int
	if err := a.db.QueryRow(`SELECT enabled FROM api_keys WHERE id=?`, id).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatal("key must be disabled after an auth rejection")
	}
}

func TestZhihuQuotaExhaustionIsNotTransient(t *testing.T) {
	a := testApp(t)
	up := zhihuMock(t, 30001, 0, "")
	defer up.Close()
	if _, err := a.db.Exec(`UPDATE providers SET enabled=1,base_url=? WHERE id='zhihu'`, up.URL); err != nil {
		t.Fatal(err)
	}
	id, err := a.addKey("zhihu", "main", "test-zhihu-secret", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w := call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["zhihu"],"max_results":3,"no_cache":true}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body)
	}
	var out SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Attempts) == 0 || !strings.Contains(out.Attempts[0].Error, "daily quota exhausted") {
		t.Fatalf("quota must be classified, got %+v", out.Attempts)
	}
	var cooldown int64
	var enabled int
	if err := a.db.QueryRow(`SELECT cooldown_until,enabled FROM api_keys WHERE id=?`, id).Scan(&cooldown, &enabled); err != nil {
		t.Fatal(err)
	}
	if cooldown < time.Now().Add(time.Hour).Unix() {
		t.Fatalf("exhausted quota must park the key beyond an hour, got %d", cooldown)
	}
	if enabled != 1 {
		t.Fatal("quota exhaustion is account state, not a bad credential: the key stays enabled")
	}
}

func TestZhihuRateLimitIsShortCooldown(t *testing.T) {
	a := testApp(t)
	up := zhihuMock(t, 30001, 4000, "")
	defer up.Close()
	if _, err := a.db.Exec(`UPDATE providers SET enabled=1,base_url=? WHERE id='zhihu'`, up.URL); err != nil {
		t.Fatal(err)
	}
	id, err := a.addKey("zhihu", "main", "test-zhihu-secret", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w := call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["zhihu"],"max_results":3,"no_cache":true}`)
	var out SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Attempts) == 0 || !strings.Contains(out.Attempts[0].Error, "rate limited") {
		t.Fatalf("burst limit must stay a rate limit, got %+v", out.Attempts)
	}
	var cooldown int64
	if err := a.db.QueryRow(`SELECT cooldown_until FROM api_keys WHERE id=?`, id).Scan(&cooldown); err != nil {
		t.Fatal(err)
	}
	if cooldown > time.Now().Add(10*time.Minute).Unix() {
		t.Fatalf("a burst limit must not park the key for long, got %d", cooldown)
	}
}

func TestDoubaoProviderSearchAndQuotaCodes(t *testing.T) {
	const item = `{"Title":"Go 官方教程","SiteName":"Go","Url":"https://go.dev/doc/tutorial/getting-started","Snippet":"short","Summary":"longer relevant summary","PublishTime":"2025-05-30T19:35:24+08:00","AuthorInfo":{"Name":"Go Team"}}`
	a := testApp(t)
	up := doubaoMock(t, 0, "", item)
	defer up.Close()
	if _, err := a.db.Exec(`UPDATE providers SET enabled=1,base_url=? WHERE id='doubao'`, up.URL); err != nil {
		t.Fatal(err)
	}
	id, err := a.addKey("doubao", "main", "test-doubao-secret", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	w := call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go 教程","providers":["doubao"],"max_results":5,"no_cache":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out SearchOutput
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != 1 || out.Results[0].URL != "https://go.dev/doc/tutorial/getting-started" || out.Results[0].Snippet != "longer relevant summary" {
		t.Fatalf("unexpected results: %+v", out.Results)
	}
	if out.Results[0].Author != "Go Team" || out.Results[0].PublishedAt != "2025-05-30T19:35:24+08:00" {
		t.Fatalf("metadata not normalized: %+v", out.Results[0])
	}

	// 10406 免费额度用尽: long park, key stays enabled, not retried as a 429.
	up2 := doubaoMock(t, 10406, "", "")
	defer up2.Close()
	if _, err := a.db.Exec(`UPDATE providers SET base_url=? WHERE id='doubao'`, up2.URL); err != nil {
		t.Fatal(err)
	}
	a.invalidate()
	w = call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["doubao"],"max_results":3,"no_cache":true}`)
	var failed SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &failed)
	if len(failed.Attempts) == 0 || !strings.Contains(failed.Attempts[0].Error, "free quota exhausted") {
		t.Fatalf("free quota must be classified, got %+v", failed.Attempts)
	}
	var cooldown int64
	if err := a.db.QueryRow(`SELECT cooldown_until FROM api_keys WHERE id=?`, id).Scan(&cooldown); err != nil {
		t.Fatal(err)
	}
	if cooldown < time.Now().Add(24*time.Hour).Unix() {
		t.Fatalf("monthly quota exhaustion must park the key for at least a day, got %d", cooldown)
	}

	// 700429 QPS limit: short cooldown.
	up3 := doubaoMock(t, 700429, "", "")
	defer up3.Close()
	if _, err := a.db.Exec(`UPDATE providers SET base_url=? WHERE id='doubao'`, up3.URL); err != nil {
		t.Fatal(err)
	}
	a.invalidate()
	if _, err := a.db.Exec(`UPDATE api_keys SET enabled=1,cooldown_until=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w = call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["doubao"],"max_results":3,"no_cache":true}`)
	var limited SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &limited)
	if len(limited.Attempts) == 0 || !strings.Contains(limited.Attempts[0].Error, "rate limited") {
		t.Fatalf("QPS limit must stay a rate limit, got %+v", limited.Attempts)
	}

	// Invalid API key: disable the key instead of leaving it in rotation.
	up4 := doubaoMock(t, 700901, "invalid_api_key", "")
	defer up4.Close()
	if _, err := a.db.Exec(`UPDATE providers SET base_url=? WHERE id='doubao'`, up4.URL); err != nil {
		t.Fatal(err)
	}
	a.invalidate()
	if _, err := a.db.Exec(`UPDATE api_keys SET enabled=1,cooldown_until=0 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	w = call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["doubao"],"max_results":3,"no_cache":true}`)
	var denied SearchOutput
	_ = json.Unmarshal(w.Body.Bytes(), &denied)
	if len(denied.Attempts) == 0 || !strings.Contains(denied.Attempts[0].Error, "auth rejected") {
		t.Fatalf("invalid key must be classified as auth, got %+v", denied.Attempts)
	}
	var enabled int
	if err := a.db.QueryRow(`SELECT enabled FROM api_keys WHERE id=?`, id).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 {
		t.Fatal("invalid api key must disable the key")
	}
}

// Zhihu and Doubao hits share the same fusion pipeline as every other
// provider: canonical-URL dedup, per-provider ranks, one vote each.
func TestZhihuAndDoubaoHitsMergeInFusion(t *testing.T) {
	rows := map[string][]Hit{
		"zhihu":  {{Title: "知乎回答", URL: "https://www.zhihu.com/question/1/answer/2?utm_source=openapi", Snippet: "短摘要"}},
		"doubao": {{Title: "同一页", URL: "https://www.zhihu.com/question/1/answer/2", Snippet: "更长的摘要文本"}, {Title: "其他", URL: "https://go.dev/doc/"}},
	}
	out := fuse(rows, SearchInput{MaxResults: 8})
	if len(out) != 2 {
		t.Fatalf("dedup failed: %+v", out)
	}
	merged := out[0]
	if merged.URL != "https://www.zhihu.com/question/1/answer/2" || len(merged.Providers) != 2 {
		t.Fatalf("same URL from both providers must merge: %+v", merged)
	}
	if merged.Ranks["zhihu"] != 1 || merged.Ranks["doubao"] != 1 {
		t.Fatalf("ranking provenance lost: %+v", merged.Ranks)
	}
	if merged.Snippet != "更长的摘要文本" {
		t.Fatalf("longer snippet must win: %q", merged.Snippet)
	}
	if merged.Score <= out[1].Score {
		t.Fatalf("two providers must outrank a single vote: %v vs %v", merged.Score, out[1].Score)
	}
	if out[1].Providers[0] != "doubao" || out[1].Ranks["doubao"] != 2 {
		t.Fatalf("unexpected second hit: %+v", out[1])
	}
}

// Credentials must never reach the request log details.
func TestProviderKeysNeverReachRequestLog(t *testing.T) {
	const item = `{"Title":"Go","ContentText":"x","Url":"https://go.dev/doc/","AuthorName":"a","CreateTime":1710000000}`
	a := testApp(t)
	up := zhihuMock(t, 0, 5000, item)
	defer up.Close()
	if _, err := a.db.Exec(`UPDATE providers SET enabled=1,base_url=? WHERE id='zhihu'`, up.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := a.addKey("zhihu", "main", "test-zhihu-secret", 1, 2); err != nil {
		t.Fatal(err)
	}
	_ = call(a, http.MethodPost, "/v1/search", a.clientToken, `{"query":"Go","providers":["zhihu"],"no_cache":true}`)
	rows, err := a.db.Query(`SELECT operation,query,details FROM requests`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var op, q, details string
		if err := rows.Scan(&op, &q, &details); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(details, "test-zhihu-secret") || strings.Contains(q, "test-zhihu-secret") {
			t.Fatalf("credential leaked into request log for %s", op)
		}
	}
}


