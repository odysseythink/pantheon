# Header-Aware Retry Middleware Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `extensions/retry` respect rate-limit HTTP headers (`retry-after`, `retry-after-ms`, `x-ratelimit-*`) for smarter backoff, and retry transient network errors.

**Architecture:** Add `Headers http.Header` to `core.ProviderError`, populate it from `core.HttpClientCall` on `>= 400`. Create a new `headerDelay` function in `extensions/retry` that parses headers with priority fallback. Integrate into `retry.Model.computeDelay`. Extend `extensions/errors.Classify` to recognize `net.Error` as retryable.

**Tech Stack:** Go 1.24, standard library (`net/http`, `time`, `strconv`)

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `core/errors.go` | Modify | `ProviderError` gains `Headers http.Header` |
| `core/http.go` | Modify | `>= 400` errors include `resp.Header.Clone()` |
| `core/http_test.go` | Modify | Test header propagation on error |
| `extensions/errors/classifier.go` | Modify | Add `KindNetwork`, `net.Error` classification |
| `extensions/errors/classifier_test.go` | Modify | Test `net.Error` classification |
| `extensions/retry/header_delay.go` | **Create** | Parse HTTP headers to compute optimal retry delay |
| `extensions/retry/header_delay_test.go` | **Create** | Unit tests for all header combinations |
| `extensions/retry/model.go` | Modify | `computeDelay` calls `headerDelay`; `shouldRetry` uses `Classify` |
| `extensions/retry/model_test.go` | Modify | Integration tests for header-aware + network retries |

---

### Task 1: core.ProviderError + HttpClientCall headers

**Files:**
- Modify: `core/errors.go`
- Modify: `core/http.go:119-125`
- Test: `core/http_test.go`

**Context:** `core/errors.go` defines `ProviderError` with `Message`, `Code`, `Status`. `core/http.go` `HttpClientCallWithClient` returns `ProviderError` on `>= 400` without headers.

- [ ] **Step 1: Modify `core/errors.go` — add `Headers` field**

```go
type ProviderError struct {
    Message string
    Code    string
    Status  int
    Headers http.Header // NEW: full HTTP response headers on >= 400
}
```

Add `"net/http"` to imports if not present.

- [ ] **Step 2: Modify `core/http.go` — populate headers on error**

Find the `>= 400` block (~line 119):

```go
// BEFORE:
return empty_resp, &ProviderError{
    Message: string(bodyData),
    Status:  resp.StatusCode,
}

// AFTER:
return empty_resp, &ProviderError{
    Message: string(bodyData),
    Status:  resp.StatusCode,
    Headers: resp.Header.Clone(),
}
```

- [ ] **Step 3: Write test in `core/http_test.go`**

Add after `TestHttpClientCall_ErrorStatus`:

```go
func TestHttpClientCall_CarriesHeadersOnError(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Retry-After", "5")
        w.Header().Set("X-Custom-Header", "value")
        w.WriteHeader(http.StatusTooManyRequests)
        w.Write([]byte(`{"error": "rate limited"}`))
    }))
    defer server.Close()

    _, err := HttpClientCall[map[string]string](
        context.Background(),
        "POST",
        server.URL+"/test",
        nil,
        nil,
        nil,
    )
    if err == nil {
        t.Fatal("expected error")
    }
    pe, ok := err.(*ProviderError)
    if !ok {
        t.Fatalf("expected ProviderError, got %T", err)
    }
    if pe.Status != 429 {
        t.Errorf("expected status 429, got %d", pe.Status)
    }
    if pe.Headers == nil {
        t.Fatal("expected Headers to be set")
    }
    if pe.Headers.Get("Retry-After") != "5" {
        t.Errorf("expected Retry-After 5, got %q", pe.Headers.Get("Retry-After"))
    }
    if pe.Headers.Get("X-Custom-Header") != "value" {
        t.Errorf("expected X-Custom-Header value, got %q", pe.Headers.Get("X-Custom-Header"))
    }
}
```

- [ ] **Step 4: Run tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/... -run TestHttpClientCall -v
```

Expected: All pass, including the new `TestHttpClientCall_CarriesHeadersOnError`.

- [ ] **Step 5: Commit**

```bash
git add core/errors.go core/http.go core/http_test.go
git commit -m "feat(core): ProviderError carries HTTP response headers"
```

---

### Task 2: extensions/errors — net.Error classification

**Files:**
- Modify: `extensions/errors/classifier.go`
- Test: `extensions/errors/classifier_test.go`

**Context:** `classifier.go` has `Kind` constants and `Classify` function. It handles `ProviderError`, context errors, and `io.ErrUnexpectedEOF`. No `net.Error` handling exists.

- [ ] **Step 1: Write failing test in `extensions/errors/classifier_test.go`**

Add to the existing `tests` slice in `TestClassifyProviderError`:

```go
// Add these imports if missing:
// "net"
// "syscall"

// Add to the tests slice:
{"DNS timeout", &net.DNSError{IsTimeout: true}, KindNetwork, true},
{"connection refused", &net.OpError{Err: syscall.ECONNREFUSED}, KindNetwork, false},
```

Add the import block additions:

```go
import (
    "context"
    "errors"
    "fmt"
    "io"
    "net"      // NEW
    "syscall"  // NEW
    "testing"

    "github.com/odysseythink/pantheon/core"
)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/errors/... -v
```

Expected: FAIL — `KindNetwork` undefined, tests for `net.Error` fail with wrong kind.

- [ ] **Step 3: Modify `extensions/errors/classifier.go`**

Add `KindNetwork` constant:

```go
const (
    // ... existing kinds ...
    KindNetwork Kind = "network" // NEW
)
```

Add `net.Error` branch in `Classify`, after the `io.ErrUnexpectedEOF` check and before the `ProviderError` check:

```go
// Add to imports: "net"

// In Classify function, after io.ErrUnexpectedEOF check:
// Network-level errors (DNS, TCP timeouts, etc.)
var netErr net.Error
if errors.As(err, &netErr) {
    if netErr.Timeout() || netErr.Temporary() {
        return Classification{Kind: KindNetwork, Retryable: true}
    }
    return Classification{Kind: KindNetwork, Retryable: false}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/errors/... -v
```

Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add extensions/errors/classifier.go extensions/errors/classifier_test.go
git commit -m "feat(errors): classify transient net.Error as retryable"
```

---

### Task 3: extensions/retry/header_delay.go

**Files:**
- **Create:** `extensions/retry/header_delay.go`
- **Create:** `extensions/retry/header_delay_test.go`

**Context:** New file. No existing code. This function parses HTTP response headers to determine the optimal retry delay.

- [ ] **Step 1: Write failing test in `extensions/retry/header_delay_test.go`**

```go
package retry

import (
    "net/http"
    "testing"
    "time"
)

func h(key, value string) http.Header {
    return http.Header{key: []string{value}}
}

func TestHeaderDelay(t *testing.T) {
    now := time.Now()
    future := now.Add(5 * time.Second).Format(time.RFC1123)
    past := now.Add(-5 * time.Second).Format(time.RFC1123)

    tests := []struct {
        name     string
        headers  http.Header
        fallback time.Duration
        want     time.Duration
    }{
        {"retry-after-ms valid", h("retry-after-ms", "1500"), 1 * time.Second, 1500 * time.Millisecond},
        {"retry-after seconds", h("retry-after", "3"), 1 * time.Second, 3 * time.Second},
        {"retry-after RFC1123 future", h("retry-after", future), 1 * time.Second, 5 * time.Second},
        {"retry-after RFC1123 past", h("retry-after", past), 1 * time.Second, 1 * time.Second},
        {"retry-after-ms unreasonable (>60s)", h("retry-after-ms", "120000"), 2 * time.Second, 2 * time.Second},
        {"retry-after unreasonable (>60s)", h("retry-after", "120"), 2 * time.Second, 2 * time.Second},
        {"x-ratelimit-reset-requests valid", h("x-ratelimit-reset-requests", "5"), 1 * time.Second, 5 * time.Second},
        {"x-ratelimit-reset-tokens valid", h("x-ratelimit-reset-tokens", "3"), 1 * time.Second, 3 * time.Second},
        {"x-ratelimit-reset unreasonable", h("x-ratelimit-reset-requests", "120"), 2 * time.Second, 2 * time.Second},
        {"preemptive slowdown remaining-requests=1", h("x-ratelimit-remaining-requests", "1"), 2 * time.Second, 3 * time.Second},
        {"preemptive slowdown remaining-tokens=2", h("x-ratelimit-remaining-tokens", "2"), 2 * time.Second, 3 * time.Second},
        {"preemptive slowdown remaining=3 no effect", h("x-ratelimit-remaining-requests", "3"), 2 * time.Second, 2 * time.Second},
        {"retry-after-ms takes priority over retry-after", func() http.Header {
            hh := h("retry-after-ms", "500")
            hh.Set("retry-after", "10")
            return hh
        }(), 1 * time.Second, 500 * time.Millisecond},
        {"retry-after takes priority over ratelimit-reset", func() http.Header {
            hh := h("retry-after", "2")
            hh.Set("x-ratelimit-reset-requests", "10")
            return hh
        }(), 1 * time.Second, 2 * time.Second},
        {"no headers uses fallback", nil, 2 * time.Second, 2 * time.Second},
        {"empty headers uses fallback", http.Header{}, 2 * time.Second, 2 * time.Second},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := headerDelay(tt.headers, tt.fallback)
            // Allow 1s tolerance for RFC1123 date parsing (clock skew)
            diff := got - tt.want
            if diff < 0 {
                diff = -diff
            }
            if diff > 1*time.Second {
                t.Errorf("headerDelay() = %v, want %v (diff %v)", got, tt.want, diff)
            }
        })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/retry/... -run TestHeaderDelay -v
```

Expected: FAIL — `headerDelay` function not defined.

- [ ] **Step 3: Create `extensions/retry/header_delay.go`**

```go
package retry

import (
    "net/http"
    "strconv"
    "time"
)

// headerDelay extracts the optimal retry delay from HTTP response headers.
// Priority (highest to lowest):
//   1. retry-after-ms      — millisecond precision, used by OpenAI etc.
//   2. retry-after         — standard HTTP, seconds or RFC1123 date
//   3. x-ratelimit-reset-* — Unix epoch seconds until rate limit resets
//   4. fallback            — caller's exponential backoff delay
//
// If the header-derived delay is zero, exceeds 60 seconds, or exceeds the
// fallback delay, the fallback is used instead.
func headerDelay(headers http.Header, fallback time.Duration) time.Duration {
    if headers == nil {
        return fallback
    }

    var delay time.Duration

    // Priority 1: retry-after-ms (most precise)
    if v := headers.Get("retry-after-ms"); v != "" {
        if ms, err := strconv.ParseFloat(v, 64); err == nil {
            delay = time.Duration(ms) * time.Millisecond
        }
    }

    // Priority 2: retry-after (seconds or RFC1123 date)
    if delay == 0 {
        if v := headers.Get("retry-after"); v != "" {
            if sec, err := strconv.ParseFloat(v, 64); err == nil {
                delay = time.Duration(sec) * time.Second
            } else if t, err := time.Parse(time.RFC1123, v); err == nil {
                delay = time.Until(t)
                if delay < 0 {
                    delay = 0
                }
            }
        }
    }

    // Priority 3: x-ratelimit-reset-requests / x-ratelimit-reset-tokens
    if delay == 0 {
        for _, key := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
            if v := headers.Get(key); v != "" {
                if sec, err := strconv.ParseFloat(v, 64); err == nil {
                    delay = time.Duration(sec) * time.Second
                    break
                }
            }
        }
    }

    // Preemptive slowdown: if remaining quota is very low, increase fallback
    for _, key := range []string{"x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens"} {
        if v := headers.Get(key); v != "" {
            if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 && n <= 2 {
                fallback = time.Duration(float64(fallback) * 1.5)
                break
            }
        }
    }

    // Sanity check: use header delay only if it's reasonable
    if delay > 0 && (delay < 60*time.Second || delay < fallback) {
        return delay
    }

    return fallback
}
```

- [ ] **Step 4: Run tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/retry/... -run TestHeaderDelay -v
```

Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add extensions/retry/header_delay.go extensions/retry/header_delay_test.go
git commit -m "feat(retry): header-aware delay calculation"
```

---

### Task 4: extensions/retry/model.go — integrate headerDelay

**Files:**
- Modify: `extensions/retry/model.go`
- Test: `extensions/retry/model_test.go`

**Context:** `model.go` has `computeDelay(attempt int)` which does pure exponential backoff. `shouldRetry(err)` calls `extErrors.Classify(err).Retryable`. `retry()` orchestrates the loop but doesn't pass the error to `computeDelay`.

- [ ] **Step 1: Write integration tests in `extensions/retry/model_test.go`**

Add after existing tests:

```go
// headerAwareModel returns 429 with headers on first N calls, then success.
type headerAwareModel struct {
    calls     int
    failNextN int
    headers   http.Header
}

func (m *headerAwareModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
    m.calls++
    if m.failNextN > 0 {
        m.failNextN--
        return nil, &core.ProviderError{Status: 429, Message: "rate limited", Headers: m.headers}
    }
    return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "ok"}}}}, nil
}
func (m *headerAwareModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *headerAwareModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *headerAwareModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *headerAwareModel) Provider() string { return "header-mock" }
func (m *headerAwareModel) Model() string    { return "header-mock" }

func TestModel_HeaderAwareDelay(t *testing.T) {
    inner := &headerAwareModel{failNextN: 1, headers: http.Header{"Retry-After": []string{"0.1"}}}
    m := &Model{
        Inner:      inner,
        MaxRetries: 3,
        BaseDelay:  1 * time.Second,
        Multiplier: 2.0,
    }

    start := time.Now()
    _, err := m.Generate(context.Background(), &core.Request{})
    elapsed := time.Since(start)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if inner.calls != 2 {
        t.Errorf("calls: got %d, want 2", inner.calls)
    }
    // With retry-after: 100ms, total wait should be ~100ms, not 1s+
    if elapsed > 300*time.Millisecond {
        t.Errorf("elapsed %v too long; header-aware delay not working", elapsed)
    }
}

func TestModel_HeaderAwareDelay_UnreasonableHeader(t *testing.T) {
    // Unreasonable retry-after (120s) should fallback to exponential backoff
    inner := &headerAwareModel{failNextN: 1, headers: http.Header{"Retry-After": []string{"120"}}}
    m := &Model{
        Inner:      inner,
        MaxRetries: 3,
        BaseDelay:  50 * time.Millisecond,
        Multiplier: 2.0,
    }

    start := time.Now()
    _, err := m.Generate(context.Background(), &core.Request{})
    elapsed := time.Since(start)

    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // Fallback delay is 50ms * [0.75, 1.25] = ~37-62ms. Total should be < 200ms.
    if elapsed > 200*time.Millisecond {
        t.Errorf("elapsed %v too long; unreasonable header should fallback", elapsed)
    }
}

// networkErrorModel returns a net.Error on first call, then success.
type networkErrorModel struct{ calls int }

func (m *networkErrorModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
    m.calls++
    if m.calls == 1 {
        return nil, &net.DNSError{IsTimeout: true}
    }
    return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "ok"}}}}, nil
}
func (m *networkErrorModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *networkErrorModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *networkErrorModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
    return nil, core.ErrNotImplemented
}
func (m *networkErrorModel) Provider() string { return "net-mock" }
func (m *networkErrorModel) Model() string    { return "net-mock" }

func TestModel_NetworkErrorRetry(t *testing.T) {
    inner := &networkErrorModel{}
    m := &Model{
        Inner:      inner,
        MaxRetries: 3,
        BaseDelay:  1 * time.Millisecond,
    }

    _, err := m.Generate(context.Background(), &core.Request{})
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if inner.calls != 2 {
        t.Errorf("calls: got %d, want 2 (initial + 1 retry)", inner.calls)
    }
}
```

Add `"net"` and `"net/http"` to the test file imports.

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/retry/... -run "TestModel_HeaderAwareDelay|TestModel_NetworkErrorRetry" -v
```

Expected: FAIL — `computeDelay` signature mismatch, `net.Error` not retryable.

- [ ] **Step 3: Modify `extensions/retry/model.go`**

**Change 1:** `computeDelay` signature and logic

Find:
```go
func (m *Model) computeDelay(attempt int) time.Duration {
```

Replace with:
```go
func (m *Model) computeDelay(attempt int, lastErr error) time.Duration {
```

At the end of `computeDelay`, before `return delay`, add:

```go
    // Try to use provider-suggested delay from response headers
    if lastErr != nil {
        var pe *core.ProviderError
        if errors.As(lastErr, &pe) && pe.Headers != nil {
            return headerDelay(pe.Headers, delay)
        }
    }
```

**Change 2:** `shouldRetry` simplification

Find:
```go
func (m *Model) shouldRetry(err error) bool {
    c := extErrors.Classify(err)
    return c.Retryable
}
```

This is already correct since Task 2 made `Classify` handle `net.Error`. No change needed.

**Change 3:** `retry()` pass `lastErr` to `computeDelay`

Find:
```go
		delay := m.computeDelay(attempt)
```

Replace with:
```go
		delay := m.computeDelay(attempt, lastErr)
```

- [ ] **Step 4: Run tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./extensions/retry/... -v
```

Expected: All pass, including new integration tests.

- [ ] **Step 5: Commit**

```bash
git add extensions/retry/model.go extensions/retry/model_test.go
git commit -m "feat(retry): integrate header-aware delay and network error retry"
```

---

### Task 5: Full regression test

**Files:** All modified/created files.

- [ ] **Step 1: Run all affected package tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/... ./extensions/retry/... ./extensions/errors/... -v
```

Expected: All pass.

- [ ] **Step 2: Check for lint / build issues**

```bash
cd /d/workspace/go_work/pantheon && go build ./...
```

Expected: Clean build, no errors.

- [ ] **Step 3: Commit**

```bash
git add -A
git diff --cached --stat
git commit -m "test: full regression for header-aware retry middleware"
```

---

## Self-Review

### Spec Coverage

| Spec Requirement | Implementing Task |
|---|---|
| `ProviderError` carries full HTTP response headers | Task 1 |
| `HttpClientCall` populates headers on `>= 400` | Task 1 |
| `retry-after-ms` header parsing | Task 3 |
| `retry-after` header parsing (seconds + RFC1123) | Task 3 |
| `x-ratelimit-reset-*` header parsing | Task 3 |
| Preemptive slowdown on low remaining quota | Task 3 |
| Sanity check (>60s fallback) | Task 3 |
| `computeDelay` integrates `headerDelay` | Task 4 |
| `net.Error` classified as retryable | Task 2 |
| `shouldRetry` uses unified `Classify` | Task 4 |
| Zero breaking changes | All (additive only) |

**No gaps found.**

### Placeholder Scan

- No TBD/TODO placeholders.
- All test code is complete with actual assertions.
- All implementation code is complete with exact function bodies.
- No vague steps like "add appropriate error handling."

### Type Consistency

- `ProviderError.Headers` is `http.Header` everywhere.
- `headerDelay` signature is `func(http.Header, time.Duration) time.Duration` consistently.
- `computeDelay` is updated to `func(int, error) time.Duration` consistently.
- `KindNetwork` is used consistently in classifier and tests.
