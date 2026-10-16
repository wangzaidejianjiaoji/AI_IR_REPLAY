package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type QuotaError struct {
	Status  int
	Period  string
	RetryAt time.Time
	Message string
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("model quota (%s, HTTP %d); recheck after %s: %s", e.Period, e.Status, e.RetryAt.Format(time.RFC3339), e.Message)
}

func classifyQuota(resp *http.Response, body []byte, now time.Time) *QuotaError {
	if resp == nil || (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden) {
		return nil
	}
	// Authentication/configuration failures can mention a quota endpoint. The
	// provider's structured error code takes precedence over message keywords.
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		for _, code := range []string{envelope.Error.Type, envelope.Error.Code} {
			switch strings.ToLower(code) {
			case "authentication_error", "invalid_api_key", "permission_denied", "invalid_request_error", "model_not_found":
				return nil
			}
		}
	}
	period, delay := "rate_limit", time.Minute
	if resp.StatusCode == http.StatusForbidden {
		evidence := strings.ToLower(string(body))
		evidence = strings.NewReplacer("-", " ", "_", " ").Replace(evidence)
		if !strings.Contains(evidence, "quota") && !strings.Contains(evidence, "usage limit") {
			return nil
		}
		period, delay = "unknown", 5*time.Hour
		switch {
		case strings.Contains(evidence, "5 hour"), strings.Contains(evidence, "five hour"):
			period = "five_hour"
		case strings.Contains(evidence, "weekly"), strings.Contains(evidence, "7 day"), strings.Contains(evidence, "seven day"):
			period = "weekly"
		}
	}
	retryAt := now.Add(delay)
	if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
			retryAt = now.Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil && !date.Before(now) {
			retryAt = date
		}
	}
	message := strings.TrimSpace(string(body))
	var detail struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &detail) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		var errorText string
		switch {
		case json.Unmarshal(detail.Error, &nested) == nil && nested.Message != "":
			message = nested.Message
		case json.Unmarshal(detail.Error, &errorText) == nil && errorText != "":
			message = errorText
		case detail.Message != "":
			message = detail.Message
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return &QuotaError{Status: resp.StatusCode, Period: period, RetryAt: retryAt, Message: message}
}

type quotaGuard struct {
	mu  sync.Mutex
	err *QuotaError
}

func (g *quotaGuard) blocked(now time.Time) *QuotaError {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		return nil
	}
	if !now.Before(g.err.RetryAt) {
		g.err = nil
		return nil
	}
	copy := *g.err
	return &copy
}

func (g *quotaGuard) remember(e *QuotaError) {
	if e == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rememberLocked(e)
}

// rememberLocked merges responses from requests already in flight. A later
// short rate limit must never shorten a known account quota cooldown.
func (g *quotaGuard) rememberLocked(e *QuotaError) {
	if g.err != nil && !e.RetryAt.After(g.err.RetryAt) {
		return
	}
	copy := *e
	g.err = &copy
}
