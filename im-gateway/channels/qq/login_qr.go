// Package qq: this file ports login_qr.py — the QQ Bot QR code binding flow.
//
// Phase 1: FetchQRCode creates a binding task.
// Phase 2: Poll or WaitForLogin returns the application credentials after
// confirmation in mobile QQ.
package qq

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	qqConnectBase         = "https://q.qq.com"
	qqBindCreatePath      = "/lite/create_bind_task"
	qqBindPollPath        = "/lite/poll_bind_result"
	qqBindStatusCompleted = 2
	qqBindStatusExpired   = 3
)

// QQBotQRCredentials holds the QQ Bot application credentials returned after
// QR confirmation.
type QQBotQRCredentials struct {
	AppID      string
	AppSecret  string
	UserOpenid string // empty when the platform did not return one
}

// QQBotQRCodeResponse is a QQ Bot QR binding task ready to be rendered by a
// client.
type QQBotQRCodeResponse struct {
	TaskID    string
	QrcodeURL string
}

// QQBotQRPollResult is the result of one QQ Bot QR binding status poll.
type QQBotQRPollResult struct {
	Status      string // "pending" | "success" | "expired"
	Credentials []QQBotQRCredentials
}

// QQBotQRWaitResult is the terminal result of waiting for a QR binding task.
type QQBotQRWaitResult struct {
	Connected   bool
	Credentials []QQBotQRCredentials
	Message     string
}

// httpTransportError marks transport-level failures (connection errors and
// non-2xx statuses). WaitForLogin retries these, mirroring the Python
// `except HTTPError` handling; other errors propagate.
type httpTransportError struct{ err error }

func (e *httpTransportError) Error() string { return e.err.Error() }
func (e *httpTransportError) Unwrap() error { return e.err }

// QQBotQRLogin is the two-phase QQ Bot QR binding flow.
//
// FetchQRCode creates a task and keeps its one-time AES key in memory.
// Poll or WaitForLogin consumes that task and returns the AppID and
// AppSecret after the user confirms the binding in mobile QQ. Callers own
// credential persistence; the channel never writes secrets to disk.
type QQBotQRLogin struct {
	source       string
	pollInterval float64

	mu       sync.Mutex
	bindKeys map[string]string
}

// NewQQBotQRLogin builds the login flow (mirrors QQBotQRLogin.__init__ with
// source defaulting to "octop" and poll_interval to 2.0 seconds). An error is
// returned when pollInterval is not greater than zero.
func NewQQBotQRLogin(source string, pollInterval float64) (*QQBotQRLogin, error) {
	if source == "" {
		source = "octop"
	}
	if pollInterval <= 0 {
		return nil, errors.New("poll_interval must be greater than zero")
	}
	return &QQBotQRLogin{source: source, pollInterval: pollInterval, bindKeys: map[string]string{}}, nil
}

// FetchQRCode creates a QQ Bot binding task and returns its QR target URL.
func (l *QQBotQRLogin) FetchQRCode(ctx context.Context) (*QQBotQRCodeResponse, error) {
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(rawKey)
	response, err := l.postJSON(ctx, qqBindCreatePath, map[string]string{"key": key})
	if err != nil {
		return nil, err
	}
	data, err := qrResponseData(response, "create bind task")
	if err != nil {
		return nil, err
	}
	taskID := ""
	if v, ok := data["task_id"]; ok && truthy(v) {
		taskID = toStringValue(v)
	}
	if taskID == "" {
		return nil, errors.New("QQ Bot create bind task response missing task_id")
	}

	l.mu.Lock()
	l.bindKeys[taskID] = key
	l.mu.Unlock()
	query := url.Values{}
	query.Set("task_id", taskID)
	query.Set("source", l.source)
	query.Set("_wv", "2")
	return &QQBotQRCodeResponse{
		TaskID:    taskID,
		QrcodeURL: qqConnectBase + "/qqbot/openclaw/connect.html?" + query.Encode(),
	}, nil
}

// Poll polls one binding task without blocking between requests.
func (l *QQBotQRLogin) Poll(ctx context.Context, taskID string) (*QQBotQRPollResult, error) {
	l.mu.Lock()
	key, ok := l.bindKeys[taskID]
	l.mu.Unlock()
	if !ok {
		return nil, errors.New("Unknown or expired QQ Bot QR task")
	}

	response, err := l.postJSON(ctx, qqBindPollPath, map[string]string{"task_id": taskID})
	if err != nil {
		return nil, err
	}
	data, err := qrResponseData(response, "poll bind result")
	if err != nil {
		return nil, err
	}
	status := 0
	if v, ok := data["status"]; ok && truthy(v) {
		status = coerceInt(v)
	}

	if status == qqBindStatusCompleted {
		appID := ""
		if v, ok := data["bot_appid"]; ok && truthy(v) {
			appID = toStringValue(v)
		}
		encryptedSecret := ""
		if v, ok := data["bot_encrypt_secret"]; ok && truthy(v) {
			encryptedSecret = toStringValue(v)
		}
		if appID == "" || encryptedSecret == "" {
			return nil, errors.New("QQ Bot bind result missing application credentials")
		}
		secret, err := decryptQQBindSecret(encryptedSecret, key)
		if err != nil {
			return nil, err
		}
		credentials := QQBotQRCredentials{AppID: appID, AppSecret: secret}
		if v, ok := data["user_openid"]; ok && truthy(v) {
			credentials.UserOpenid = toStringValue(v)
		}
		l.mu.Lock()
		delete(l.bindKeys, taskID)
		l.mu.Unlock()
		return &QQBotQRPollResult{Status: "success", Credentials: []QQBotQRCredentials{credentials}}, nil
	}

	if status == qqBindStatusExpired {
		l.mu.Lock()
		delete(l.bindKeys, taskID)
		l.mu.Unlock()
		return &QQBotQRPollResult{Status: "expired"}, nil
	}

	return &QQBotQRPollResult{Status: "pending"}, nil
}

// WaitForLogin waits until a QR task succeeds, expires, or reaches the
// timeout (mirrors wait_for_login; timeoutS defaults to 480 seconds in
// Python — callers pass the value explicitly here).
func (l *QQBotQRLogin) WaitForLogin(ctx context.Context, taskID string, timeoutS float64) (*QQBotQRWaitResult, error) {
	deadline := time.Now().Add(time.Duration(timeoutS * float64(time.Second)))
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline).Seconds()
		result, err := l.Poll(ctx, taskID)
		if err != nil {
			var te *httpTransportError
			if errors.As(err, &te) {
				logf("WARN", "QQ Bot QR poll failed; retrying: %s", err)
				l.sleepRemaining(ctx, remaining)
				continue
			}
			return nil, err
		}
		if result.Status == "success" {
			return &QQBotQRWaitResult{Connected: true, Credentials: result.Credentials, Message: "QR code confirmed"}, nil
		}
		if result.Status == "expired" {
			return &QQBotQRWaitResult{Connected: false, Message: "QR code expired"}, nil
		}
		l.sleepRemaining(ctx, remaining)
	}

	l.Cancel(taskID)
	return &QQBotQRWaitResult{
		Connected: false,
		Message:   fmt.Sprintf("QR login timeout after %s seconds", strconv.FormatFloat(timeoutS, 'g', -1, 64)),
	}, nil
}

// Cancel forgets a pending QR task and its one-time decryption key.
func (l *QQBotQRLogin) Cancel(taskID string) {
	l.mu.Lock()
	delete(l.bindKeys, taskID)
	l.mu.Unlock()
}

// sleepRemaining sleeps min(poll_interval, max(0, remaining)) seconds.
func (l *QQBotQRLogin) sleepRemaining(ctx context.Context, remaining float64) {
	d := l.pollInterval
	if remaining < d {
		d = remaining
	}
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(time.Duration(d * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (l *QQBotQRLogin) postJSON(ctx context.Context, path string, payload map[string]string) (map[string]any, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, qqConnectBase+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &httpTransportError{err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &httpTransportError{err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpTransportError{fmt.Errorf("http status %d: %s", resp.StatusCode, truncate(string(raw), 200))}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("QQ Bot QR endpoint returned a malformed response")
	}
	return body, nil
}

// qrResponseData mirrors _response_data: retcode must be 0 and data a dict.
func qrResponseData(response map[string]any, action string) (map[string]any, error) {
	retcode := 0
	if v, ok := response["retcode"]; ok && truthy(v) {
		retcode = coerceInt(v)
	}
	if retcode != 0 {
		msg := "unknown error"
		if v, ok := response["msg"]; ok && truthy(v) {
			msg = toStringValue(v)
		}
		return nil, fmt.Errorf("QQ Bot %s failed: %s", action, msg)
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("QQ Bot %s response missing data", action)
	}
	return data, nil
}

// decryptQQBindSecret decrypts the AES-GCM protected bot secret.
// nonce is the first 12 bytes; the key is 32 raw bytes (base64 encoded).
func decryptQQBindSecret(encryptedSecret, key string) (string, error) {
	encrypted, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encryptedSecret))
	if err != nil {
		return "", fmt.Errorf("QQ Bot bind result contains invalid encrypted credentials: %w", err)
	}
	rawKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil {
		return "", fmt.Errorf("QQ Bot bind result contains invalid encrypted credentials: %w", err)
	}
	if len(encrypted) < 29 || len(rawKey) != 32 {
		return "", errors.New("QQ Bot bind result contains invalid encrypted credentials")
	}
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, encrypted[:12], encrypted[12:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// coerceInt mirrors Python int(str(v)): handles JSON numbers and numeric
// strings.
func coerceInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
