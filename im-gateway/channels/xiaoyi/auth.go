// Package xiaoyi — AK/SK authentication helpers.
//
// Ports octop_gateway.channels.xiaoyi.auth: HMAC-SHA256 signature over the
// millisecond timestamp, Base64-encoded, sent as WebSocket upgrade headers.
package xiaoyi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// generateSignature mirrors auth.generate_signature:
// Base64(HMAC-SHA256(sk, timestamp)).
func generateSignature(sk, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(sk))
	mac.Write([]byte(timestamp))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// generateAuthHeaders mirrors auth.generate_auth_headers: build the
// WebSocket authentication headers for XiaoYi.
func generateAuthHeaders(ak, sk, agentID string) map[string]string {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	signature := generateSignature(sk, timestamp)
	return map[string]string{
		"x-access-key": ak,
		"x-sign":       signature,
		"x-ts":         timestamp,
		"x-agent-id":   agentID,
	}
}
