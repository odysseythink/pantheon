# Header-Aware Retry Middleware Design

**Date:** 2026-05-25
**Scope:** `core`, `extensions/retry`, `extensions/errors`
**Status:** Approved

## Background & Motivation

Pantheon's current `extensions/retry` wrapper uses pure exponential backoff with jitter. It does not inspect HTTP response headers, so when a provider returns `429 Too Many Requests` with a `Retry-After: 5` header, the retry layer ignores that hint and waits according to its own schedule (e.g. 1s, 2s, 4s). This wastes quota and increases latency.

Fantasy solves this by carrying `ResponseHeaders` in `ProviderError` and preferring `retry-after-ms` / `retry-after` over exponential backoff. We want to bring the same capability to Pantheon, with enhancements:

- **Full header preservation** (`http.Header`) for observability and future extensibility.
- **Smart backoff beyond `retry-after`** — fallback to `x-ratelimit-reset-*` when `retry-after` is absent, and preemptive slowdown when remaining quota is low.
- **Network-level error retry** — DNS failures, TCP timeouts, etc. are also transient and should be retried.

## Goals

1. `core.ProviderError` carries the full HTTP response headers on `>= 400` errors.
2. `extensions/retry` reads `retry-after-ms`, `retry-after`, and `x-ratelimit-*` headers to compute the optimal retry delay.
3. `extensions/errors.Classify` recognizes transient `net.Error` as retryable.
4. Zero breaking changes to existing APIs.

## Non-Goals

- Provider-specific header normalization (e.g. mapping Azure's `x-ms-ratelimit-*` to a common schema). We parse standard headers directly.
- Caching or cross-request rate-limit state. Each request is independent.
- Modifying the `core.HttpClientCall` success path. Headers are only captured on errors.

## Design Overview

```
Provider HTTP call fails
  → core.HttpClientCall captures resp.Header into ProviderError
    → retry.Model.shouldRetry sees retryable error (incl. net.Error)
      → retry.Model.computeDelay calls headerDelay(err.Headers, fallback)
        → headerDelay parses headers, returns optimal delay
          → sleep → retry
```

### Files Changed

| File | Action | Description |
|---|---|---|
| `core/errors.go` | Modify | `ProviderError` adds `Headers http.Header` |
| `core/http.go` | Modify | `>= 400` errors include `resp.Header.Clone()` |
| `core/http_test.go` | Modify | Test header propagation |
| `extensions/retry/header_delay.go` | **Add** | Header parsing + delay calculation |
| `extensions/retry/header_delay_test.go` | **Add** | Table-driven unit tests for header parsing |
| `extensions/retry/model.go` | Modify | `computeDelay` calls `headerDelay`; `shouldRetry` uses `Classify` |
| `extensions/retry/model_test.go` | Modify | Integration tests for header-aware + network retries |
| `extensions/errors/classifier.go` | Modify | Add `KindNetwork` + `net.Error` branch |
| `extensions/errors/classifier_test.go` | Modify | Add `net.Error` classification cases |

## Detailed Design

### 1. Core: ProviderError + HttpClientCall

#### 1.1 ProviderError

```go
type ProviderError struct {
    Message string
    Code    string
    Status  int
    Headers http.Header  // NEW: full HTTP response headers on >= 400
}
```

- `Headers` is `nil` when the error is not from an HTTP response (e.g. local JSON decode failure).
- Using `http.Header` instead of `map[string]string` handles multi-value headers correctly and provides case-insensitive lookup via `Get()`.

#### 1.2 ProviderError.IsRetryable()

No change. The existing status-code logic is sufficient for Pantheon's current providers. The `x-should-retry` header (used by some providers as an explicit retry signal) can be added later without breaking changes, since `ProviderError.Headers` will already be available.

#### 1.3 HttpClientCallWithClient

On `resp.StatusCode >= 400`:

```go
bodyData, _ := io.ReadAll(resp.Body)
return empty_resp, &ProviderError{
    Message: string(bodyData),
    Status:  resp.StatusCode,
    Headers: resp.Header.Clone(),
}
```

`resp.Header.Clone()` performs a deep copy so the header map remains valid after `resp.Body.Close()`.

### 2. Retry Layer

#### 2.1 headerDelay function (new file: `extensions/retry/header_delay.go`)

```go
func headerDelay(headers http.Header, fallback time.Duration) time.Duration
```

**Priority order (highest → lowest):**

| Priority | Header | Parse | Sanity Check |
|---|---|---|---|
| 1 | `retry-after-ms` | `ParseFloat` → `ms * time.Millisecond` | `0 < delay < 60s` OR `delay < fallback` |
| 2 | `retry-after` | Seconds (`ParseFloat`) or RFC1123 date | Same |
| 3 | `x-ratelimit-reset-requests` | Unix epoch seconds → `time.Until(time.Unix(val, 0))` | Same |
| 4 | `x-ratelimit-reset-tokens` | Unix epoch seconds → `time.Until(time.Unix(val, 0))` | Same |
| 5 | `fallback` | Exponential backoff from caller | Naturally bounded |

**Sanity check rationale:** If a header suggests waiting > 60 seconds, it is likely a malformed value or a "back off indefinitely" signal. In either case, falling back to our own bounded exponential backoff is safer.

**Preemptive slowdown:** If `x-ratelimit-remaining-requests` or `x-ratelimit-remaining-tokens` is present and `<= 2`, multiply the `fallback` delay by `1.5×`. This reduces the chance of hitting the rate limit on the next request.

#### 2.2 model.go changes

`computeDelay` gains an `err` parameter so it can inspect the last error's headers:

```go
func (m *Model) computeDelay(attempt int, lastErr error) time.Duration {
    // ... existing exponential backoff + jitter ...

    if lastErr != nil {
        var pe *core.ProviderError
        if errors.As(lastErr, &pe) && pe.Headers != nil {
            return headerDelay(pe.Headers, delay)
        }
    }
    return delay
}
```

`shouldRetry` delegates entirely to `extErrors.Classify` (see Section 3):

```go
func (m *Model) shouldRetry(err error) bool {
    return extErrors.Classify(err).Retryable
}
```

`retry()` passes `lastErr` into `computeDelay`:

```go
delay := m.computeDelay(attempt, lastErr)
```

### 3. Error Classification (extensions/errors)

#### 3.1 New Kind

```go
const KindNetwork Kind = "network"
```

#### 3.2 Classify enhancement

```go
func Classify(err error) Classification {
    // ... existing checks ...

    var netErr net.Error
    if errors.As(err, &netErr) {
        if netErr.Timeout() || netErr.Temporary() {
            return Classification{Kind: KindNetwork, Retryable: true}
        }
        return Classification{Kind: KindNetwork, Retryable: false}
    }

    return Classification{Kind: KindUnknown, Retryable: false}
}
```

This centralizes retryability decisions. Any consumer of `Classify` (retry wrapper, CLI plugins, observability) automatically understands network errors.

> **Note:** `net.Error.Temporary()` is deprecated in Go 1.18+, but the standard library's concrete network errors still implement it meaningfully. We use it as a practical signal for transient failures.

## Testing Strategy

### Unit Tests

1. **`core/http_test.go`**: `TestHttpClientCall_CarriesHeadersOnError` — mock server returns 429 + `Retry-After: 5`, assert `ProviderError.Headers.Get("Retry-After") == "5"`.

2. **`extensions/retry/header_delay_test.go`**: Table-driven tests covering:
   - `retry-after-ms` (valid, unreasonable, missing)
   - `retry-after` (seconds, RFC1123 date, unreasonable)
   - `x-ratelimit-reset-*` (valid seconds)
   - `x-ratelimit-remaining-*` (triggers preemptive slowdown)
   - No headers (falls back, verifies jitter range)

3. **`extensions/errors/classifier_test.go`**: Add:
   - `&net.DNSError{IsTimeout: true}` → `KindNetwork, Retryable: true`
   - `&net.OpError{Err: syscall.ECONNREFUSED}` → `KindNetwork, Retryable: false`

### Integration Tests

4. **`extensions/retry/model_test.go`**:
   - `TestModel_HeaderAwareDelay` — mock model returns 429 + `retry-after: 100ms`. Verify retry waits ~100ms instead of the default 1s base delay.
   - `TestModel_NetworkErrorRetry` — mock model returns a timeout `*net.DNSError`. Verify it is retried.

### Compatibility

- **Zero API breakage**: `ProviderError` gains a field; existing field access is unchanged. `retry.Model` gains no new exported fields. `Classify` returns a new possible `Kind` value, which is additive.
- **Behavior change**: Users of `extensions/retry` will see smarter delays on 429 errors. This is purely an improvement.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| Malicious/broken `retry-after` header suggests 0ms → tight loop | Sanity check: `delay > 0` required; `0` falls back to exponential backoff. |
| `resp.Header.Clone()` copies all headers including large ones | Headers are typically < 1KB. Only copied on error paths. Acceptable overhead. |
| Provider uses non-standard header names | We parse standard headers (`retry-after*`, `x-ratelimit-*`). Non-standard headers are preserved in `ProviderError.Headers` for user inspection but do not affect delay. |
| `net.Error.Temporary()` removed in future Go | Compile-time break. If Go removes the method, we switch to checking concrete error types (`*net.DNSError`, `*net.OpError`) directly. |
