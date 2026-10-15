package llm

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassifyQuota(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body, retryAfter, period string
		status                         int
		delay                          time.Duration
	}{
		{"rate limit", `{"error":{"message":"Too many requests"}}`, "", "rate_limit", 429, time.Minute},
		{"seconds", `busy`, "120", "rate_limit", 429, 2 * time.Minute},
		{"date", `busy`, "Thu, 15 Oct 2026 02:03:00 GMT", "rate_limit", 429, 3 * time.Minute},
		{"invalid retry", `busy`, "tomorrow", "rate_limit", 429, time.Minute},
		{"negative retry", `busy`, "-1", "rate_limit", 429, time.Minute},
		{"five hour", `{"error":{"message":"You have reached your 5-hour usage limit"}}`, "", "five_hour", 403, 5 * time.Hour},
		{"five hours", `5 hours usage limit exceeded`, "", "five_hour", 403, 5 * time.Hour},
		{"weekly", `Weekly usage limit reached`, "", "weekly", 403, 5 * time.Hour},
		{"seven day", `Your 7-day usage limit was reached`, "", "weekly", 403, 5 * time.Hour},
		{"generic", `Quota exceeded`, "", "unknown", 403, 5 * time.Hour},
		{"generic usage", `Usage limit exceeded`, "", "unknown", 403, 5 * time.Hour},
		{"malformed error JSON", `{"error":{"message":"weekly usage limit exceeded`, "", "weekly", 403, 5 * time.Hour},
		{"ordinary forbidden", `Forbidden`, "", "", 403, 0},
		{"number is not status", `forbidden request 429`, "", "", 403, 0},
		{"weekly access", `Weekly report access forbidden`, "", "", 403, 0},
		{"unauthorized", `quota exceeded`, "", "", 401, 0},
		{"server error", `quota exceeded`, "", "", 500, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
			resp.Header.Set("Retry-After", tc.retryAfter)
			got := classifyQuota(resp, []byte(tc.body), now)
			if tc.period == "" {
				if got != nil {
					t.Fatalf("ordinary error classified as quota: %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("quota response not classified")
			}
			if got.Status != tc.status || got.Period != tc.period || !got.RetryAt.Equal(now.Add(tc.delay)) {
				t.Fatalf("classification=%+v want status=%d period=%s retry=%s", got, tc.status, tc.period, now.Add(tc.delay))
			}
		})
	}
}

func TestQuotaMessageUsesProviderDetailAndCapsLength(t *testing.T) {
	now := time.Now()
	resp := &http.Response{StatusCode: 429, Header: http.Header{"Authorization": []string{"secret"}}}
	got := classifyQuota(resp, []byte(`{"error":{"message":"Too many requests","debug":"private detail"}}`), now)
	if got == nil || got.Message != "Too many requests" {
		t.Fatalf("detail=%+v", got)
	}
	if strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "private detail") {
		t.Fatalf("unexpected detail in %q", got.Error())
	}
	got = classifyQuota(resp, []byte(strings.Repeat("x", 600)), now)
	if len(got.Message) > 512 {
		t.Fatalf("message length=%d", len(got.Message))
	}
	if got.Error() == "" {
		t.Fatal("missing error text")
	}
}

func TestQuotaGuardExpiresAtDeadlineAndCopiesErrors(t *testing.T) {
	now := time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC)
	var guard quotaGuard
	if guard.blocked(now) != nil {
		t.Fatal("new guard blocks requests")
	}
	q := &QuotaError{Status: 403, Period: "weekly", RetryAt: now.Add(5 * time.Hour), Message: "usage limit"}
	guard.remember(q)
	q.RetryAt = now
	got := guard.blocked(now)
	if got == nil {
		t.Fatal("mutating remembered error cleared cooldown")
	}
	got.RetryAt = now
	if guard.blocked(now.Add(5*time.Hour-time.Nanosecond)) == nil {
		t.Fatal("guard expired before deadline")
	}
	if guard.blocked(now.Add(5*time.Hour)) != nil {
		t.Fatal("guard did not expire at deadline")
	}
	if guard.blocked(now.Add(6*time.Hour)) != nil {
		t.Fatal("expired guard blocked request")
	}
	guard.remember(nil)
}

func TestQuotaGuardConcurrentAccess(t *testing.T) {
	now := time.Now()
	var guard quotaGuard
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); guard.remember(&QuotaError{RetryAt: now.Add(time.Hour)}); guard.blocked(now) }()
	}
	wg.Wait()
	if guard.blocked(now) == nil {
		t.Fatal("concurrent updates lost cooldown")
	}
}
