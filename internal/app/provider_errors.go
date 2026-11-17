package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// providerCodeError carries an upstream business error code from providers that
// answer HTTP 200 with a code in the body (Zhihu, Doubao). status follows the
// HTTP-like semantics used by the key pool: 401/403 disables the key, 402/429
// apply a cooldown, everything else only records last_error.
//
// detail is a fixed label: never interpolate upstream text, credentials or
// response bodies into it.
type providerCodeError struct {
	provider string
	code     int
	status   int
	cooldown int64
	detail   string
}

func (e providerCodeError) Error() string { return e.detail }

func codeLabel(provider string, code int, detail string) string {
	return fmt.Sprintf("upstream %s (%s %d)", detail, provider, code)
}

// nextDailyReset is the next 00:00 in the provider's quota timezone (UTC+8).
func nextDailyReset() time.Time {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
}

// nextMonthlyReset is the first day of the next month in UTC+8.
func nextMonthlyReset() time.Time {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
}

func unixTime(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

// zhihuError maps the documented codes from the Zhihu open platform. A 30001
// can be either a burst rate limit or an exhausted daily quota; the quota
// endpoint is consulted (it does not consume quota) to tell them apart, so an
// exhausted key is parked until the next reset instead of being retried as 429.
func (a *App) zhihuError(ctx context.Context, p Provider, key string, code int) error {
	switch code {
	case 20001:
		return providerCodeError{provider: "zhihu", code: code, status: 401, detail: codeLabel("zhihu", code, "auth rejected")}
	case 30001:
		if remaining, ok := a.zhihuRemaining(ctx, p, key); ok && remaining <= 0 {
			return providerCodeError{provider: "zhihu", code: code, status: 402, cooldown: nextDailyReset().Unix(), detail: codeLabel("zhihu", code, "daily quota exhausted")}
		}
		return providerCodeError{provider: "zhihu", code: code, status: 429, cooldown: time.Now().Add(time.Minute).Unix(), detail: codeLabel("zhihu", code, "rate limited")}
	case 10001:
		return providerCodeError{provider: "zhihu", code: code, status: 400, detail: codeLabel("zhihu", code, "parameter rejected")}
	default:
		return providerCodeError{provider: "zhihu", code: code, status: 502, detail: codeLabel("zhihu", code, "provider error")}
	}
}

// zhihuRemaining reads the daily quota for zhihu_search. The quota endpoint is
// free (documented as not consuming business quota) and failures are treated as
// "unknown", never as an error that fails the search.
func (a *App) zhihuRemaining(ctx context.Context, p Provider, key string) (int64, bool) {
	u, err := validURL(p.BaseURL)
	if err != nil {
		return 0, false
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/quota"
	q := u.Query()
	q.Set("APIIDs", "zhihu_search")
	u.RawQuery = q.Encode()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("Accept", "application/json")
	res, err := a.client.upstream.Do(req)
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, false
	}
	b, err := readBody(res.Body, 1<<20)
	if err != nil {
		return 0, false
	}
	var out struct {
		Code int
		Data []struct {
			APIID          string
			RemainingQuota int64
		}
	}
	if json.Unmarshal(b, &out) != nil || out.Code != 0 {
		return 0, false
	}
	for _, d := range out.Data {
		if d.APIID == "zhihu_search" {
			return d.RemainingQuota, true
		}
	}
	return 0, false
}

// doubaoError maps 豆包搜索 (联网搜索 Custom 版) codes. Quota and billing codes
// park the key for a long time: they are account state, not a transient 429.
func doubaoError(codeN int, code string) error {
	codeNum := codeN
	if codeNum == 0 {
		if n, err := strconv.Atoi(code); err == nil {
			codeNum = n
		}
	}
	if codeNum == 0 && code != "" {
		switch strings.ToLower(code) {
		case "invalid_api_key", "unauthorized":
			return providerCodeError{provider: "doubao", status: 401, detail: "upstream doubao auth rejected (" + code + ")"}
		default:
			return providerCodeError{provider: "doubao", status: 502, detail: "upstream doubao provider error (" + code + ")"}
		}
	}
	switch codeNum {
	case 10401, 700901:
		return providerCodeError{provider: "doubao", code: codeNum, status: 401, detail: codeLabel("doubao", codeNum, "auth rejected")}
	case 10403:
		return providerCodeError{provider: "doubao", code: codeNum, status: 403, detail: codeLabel("doubao", codeNum, "search service not enabled")}
	case 10406:
		return providerCodeError{provider: "doubao", code: codeNum, status: 402, cooldown: nextMonthlyReset().Unix(), detail: codeLabel("doubao", codeNum, "free quota exhausted")}
	case 10412:
		return providerCodeError{provider: "doubao", code: codeNum, status: 402, cooldown: time.Now().Add(24 * time.Hour).Unix(), detail: codeLabel("doubao", codeNum, "subscription quota exhausted")}
	case 10408, 10409, 10410:
		return providerCodeError{provider: "doubao", code: codeNum, status: 402, cooldown: time.Now().Add(24 * time.Hour).Unix(), detail: codeLabel("doubao", codeNum, "billing or plan unavailable")}
	case 700429:
		return providerCodeError{provider: "doubao", code: codeNum, status: 429, cooldown: time.Now().Add(time.Minute).Unix(), detail: codeLabel("doubao", codeNum, "rate limited")}
	case 10400:
		return providerCodeError{provider: "doubao", code: codeNum, status: 400, detail: codeLabel("doubao", codeNum, "parameter rejected")}
	default:
		return providerCodeError{provider: "doubao", code: codeNum, status: 502, detail: codeLabel("doubao", codeNum, "provider error")}
	}
}
