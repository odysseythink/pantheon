// WeChat iLink Bot QR code login flow (ports channels/weixin/login_qr.py).
//
// Phase 1: WeixinQRLogin.FetchQRCode()   → QRCodeResponse (token + display URL)
// Phase 2: WeixinQRLogin.WaitForLogin()  → WeixinQrWaitResult (connected + credentials)
package weixin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Constants ────────────────────────────────────────────────────────────────

const (
	qrAPIBaseURL       = "https://ilinkai.weixin.qq.com"
	qrBotEndpoint      = "ilink/bot/get_bot_qrcode"
	qrStatusEndpoint   = "ilink/bot/get_qrcode_status"
	defaultBotType     = "3"
	ilinkClientVersion = "1"
	qrLongPollTimeoutS = 35.0
	loginTimeoutS      = 480.0 // 8 minutes overall
	maxQRRefreshCount  = 3
)

// allowedQRHosts is the SSRF protection whitelist for QR login.
var allowedQRHosts = map[string]bool{"ilinkai.weixin.qq.com": true}

// ── Types ────────────────────────────────────────────────────────────────────

// QRCodeResponse is the response from the WeChat QR code fetch endpoint.
type QRCodeResponse struct {
	Qrcode           string // opaque polling token
	QrcodeImgContent string // URL to display as QR image
}

// qrStatusResponse is the internal raw polling response from iLink.
type qrStatusResponse struct {
	Status      string
	BotToken    *string
	IlinkBotID  *string
	Baseurl     *string
	IlinkUserID *string
}

// WeixinQrWaitResult is the result of waiting for QR code confirmation.
type WeixinQrWaitResult struct {
	Connected bool
	BotToken  *string
	AccountID *string // normalised ilink_bot_id
	BaseURL   *string // server-returned base URL for messaging
	UserID    *string
	Message   string
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// normalizeAccountID normalises WeChat ilink_bot_id by replacing '@' and '.'
// with '-'. Returns nil for empty input.
func normalizeAccountID(ilinkBotID *string) *string {
	if ilinkBotID == nil || *ilinkBotID == "" {
		return nil
	}
	out := strings.ReplaceAll(strings.ReplaceAll(*ilinkBotID, "@", "-"), ".", "-")
	return &out
}

// ── WeixinQRLogin ────────────────────────────────────────────────────────────

// WeixinQRLogin is the two-phase QR code login for WeChat iLink Bot.
type WeixinQRLogin struct {
	qrBaseURL    string
	routeTag     string
	activeQrcode string
	refreshCount int
}

// NewWeixinQRLogin builds a login helper. An empty qrBaseURL falls back to
// the default iLink API base. The URL is validated against the SSRF
// whitelist (mirrors the ValueError raised by the Python constructor).
func NewWeixinQRLogin(qrBaseURL, routeTag string) (*WeixinQRLogin, error) {
	if qrBaseURL == "" {
		qrBaseURL = qrAPIBaseURL
	}
	validated, err := validateQRBaseURL(qrBaseURL)
	if err != nil {
		return nil, err
	}
	return &WeixinQRLogin{qrBaseURL: validated, routeTag: routeTag}, nil
}

// validateQRBaseURL validates the URL to prevent SSRF attacks: only HTTPS and
// whitelisted hosts are permitted.
func validateQRBaseURL(rawURL string) (string, error) {
	rawURL = strings.TrimRight(rawURL, "/")
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid QR API URL: %v", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("Only HTTPS allowed for QR API; got scheme: %s", parsed.Scheme)
	}
	if !allowedQRHosts[parsed.Hostname()] {
		return "", fmt.Errorf("Unauthorized host for QR API: %s. Allowed: %v", parsed.Hostname(), allowedQRHosts)
	}
	return rawURL, nil
}

// qrHeaders builds the shared request headers for both QR endpoints.
func (l *WeixinQRLogin) qrHeaders() map[string]string {
	headers := map[string]string{"iLink-App-ClientVersion": ilinkClientVersion}
	if l.routeTag != "" {
		headers["SKRouteTag"] = l.routeTag
	}
	return headers
}

// FetchQRCode fetches a new QR code from iLink.
func (l *WeixinQRLogin) FetchQRCode(ctx context.Context, resetRefreshCount bool) (*QRCodeResponse, error) {
	rawURL := fmt.Sprintf("%s/%s", l.qrBaseURL, qrBotEndpoint)
	params := url.Values{"bot_type": {defaultBotType}}

	client := &http.Client{Timeout: secondsDur(15.0)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range l.qrHeaders() {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d fetching QR code: %.100s", resp.StatusCode, string(raw))
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	// QRCodeResponse(**data) requires both string fields (pydantic would
	// raise a ValidationError on a malformed response).
	qrcode, ok := data["qrcode"].(string)
	if !ok {
		return nil, fmt.Errorf("malformed QR code response: missing 'qrcode'")
	}
	imgContent, ok := data["qrcode_img_content"].(string)
	if !ok {
		return nil, fmt.Errorf("malformed QR code response: missing 'qrcode_img_content'")
	}
	result := &QRCodeResponse{Qrcode: qrcode, QrcodeImgContent: imgContent}
	l.activeQrcode = result.Qrcode
	if resetRefreshCount {
		l.refreshCount = 0
	}
	return result, nil
}

// WaitForLogin long-polls for QR status until confirmed / expired / timeout.
//
// Automatically refreshes the QR up to maxQRRefreshCount times if it expires
// before the user scans. onScanned is invoked once when the user scans
// (before confirmation). Errors from QR refresh propagate to the caller.
func (l *WeixinQRLogin) WaitForLogin(ctx context.Context, qrcode string, timeoutS float64, onScanned func()) (*WeixinQrWaitResult, error) {
	if timeoutS <= 0 {
		timeoutS = loginTimeoutS
	}
	l.activeQrcode = qrcode
	startTime := time.Now()
	scannedNotified := false

	client := &http.Client{Timeout: secondsDur(qrLongPollTimeoutS + 5)}
	for time.Since(startTime).Seconds() < timeoutS {
		if err := ctx.Err(); err != nil {
			return &WeixinQrWaitResult{Message: "QR login canceled"}, nil
		}

		rawURL := fmt.Sprintf("%s/%s", l.qrBaseURL, qrStatusEndpoint)
		params := url.Values{"qrcode": {qrcode}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL+"?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		for k, v := range l.qrHeaders() {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		var raw []byte
		var data map[string]any
		if err == nil {
			func() {
				defer func() { _ = resp.Body.Close() }()
				raw, err = io.ReadAll(resp.Body)
				if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
					err = fmt.Errorf("HTTP %d: %.100s", resp.StatusCode, string(raw))
				}
				if err == nil {
					if jerr := json.Unmarshal(raw, &data); jerr != nil {
						err = jerr
					}
				}
			}()
		}
		if err != nil {
			// httpx.HTTPError / ValueError → warn and retry.
			logf("WARN", "QR status poll failed (retrying): %v", err)
			if !sleepFor(ctx, time.Second) {
				return &WeixinQrWaitResult{Message: "QR login canceled"}, nil
			}
			continue
		}

		// A missing/non-string status mirrors the pydantic ValidationError
		// (a ValueError subclass) → warn and retry.
		statusRaw, statusOK := data["status"].(string)
		if !statusOK {
			logf("WARN", "QR status poll failed (retrying): %v", fmt.Errorf("malformed status response: %.100s", string(raw)))
			if !sleepFor(ctx, time.Second) {
				return &WeixinQrWaitResult{Message: "QR login canceled"}, nil
			}
			continue
		}
		statusResp := decodeQRStatus(data)
		status := statusResp.Status
		_ = statusRaw

		if status == "wait" {
			continue
		}

		if status == "scaned" {
			if onScanned != nil && !scannedNotified {
				scannedNotified = true
				func() {
					defer func() {
						// User callbacks must not abort the login polling loop.
						if r := recover(); r != nil {
							logf("ERROR", "on_scanned callback error: %v", r)
						}
					}()
					onScanned()
				}()
			}
			if !sleepFor(ctx, time.Second) {
				return &WeixinQrWaitResult{Message: "QR login canceled"}, nil
			}
			continue
		}

		if status == "confirmed" {
			logf("INFO", "WeChat QR login confirmed")
			return &WeixinQrWaitResult{
				Connected: true,
				BotToken:  statusResp.BotToken,
				AccountID: normalizeAccountID(statusResp.IlinkBotID),
				BaseURL:   statusResp.Baseurl,
				UserID:    statusResp.IlinkUserID,
				Message:   "QR code confirmed",
			}, nil
		}

		if status == "expired" {
			logf("INFO", "QR expired (refresh %d/%d)", l.refreshCount, maxQRRefreshCount)
			if l.refreshCount < maxQRRefreshCount {
				l.refreshCount++
				newQR, err := l.FetchQRCode(ctx, false)
				if err != nil {
					return nil, err
				}
				qrcode = newQR.Qrcode
				scannedNotified = false
				continue
			}
			return &WeixinQrWaitResult{
				Connected: false,
				Message:   fmt.Sprintf("QR expired after %d refresh attempts", maxQRRefreshCount),
			}, nil
		}

		logf("WARN", "Unknown QR status: %s", status)
		if !sleepFor(ctx, time.Second) {
			return &WeixinQrWaitResult{Message: "QR login canceled"}, nil
		}
	}

	return &WeixinQrWaitResult{
		Connected: false,
		Message:   fmt.Sprintf("QR login timeout (no confirmation within %gs)", timeoutS),
	}, nil
}

func decodeQRStatus(data map[string]any) qrStatusResponse {
	status, _ := data["status"].(string)
	return qrStatusResponse{
		Status:      status,
		BotToken:    asStrPtr(orAny(data, nil, "bot_token")),
		IlinkBotID:  asStrPtr(orAny(data, nil, "ilink_bot_id")),
		Baseurl:     asStrPtr(orAny(data, nil, "baseurl")),
		IlinkUserID: asStrPtr(orAny(data, nil, "ilink_user_id")),
	}
}
