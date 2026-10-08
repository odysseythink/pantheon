package imgateway

// Shared logging and id helpers for the gateway package.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"
)

var gatewayLogger = log.New(os.Stderr, "imgateway: ", log.LstdFlags|log.Lmsgprefix)

// SetLogger replaces the package logger (host applications may inject their own).
func SetLogger(l *log.Logger) {
	if l != nil {
		gatewayLogger = l
	}
}

func logDebug(format string, args ...any) {
	// Python logger.debug is silent by default; keep parity by skipping
	// debug output unless IMGATEWAY_DEBUG is set.
	if os.Getenv("IMGATEWAY_DEBUG") == "" {
		return
	}
	gatewayLogger.Printf("DEBUG "+format, args...)
}

func logInfo(format string, args ...any)  { gatewayLogger.Printf("INFO "+format, args...) }
func logWarn(format string, args ...any)  { gatewayLogger.Printf("WARN "+format, args...) }
func logError(format string, args ...any) { gatewayLogger.Printf("ERROR "+format, args...) }

// newUUIDHex returns a uuid4-shaped hex string (mirrors uuid.uuid4().hex).
func newUUIDHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a time-based id; crypto/rand should never fail here.
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	// Set version 4 and variant bits (RFC 4122), matching uuid4.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}

// NewUUIDHex is the exported form used by the manager and channels.
func NewUUIDHex() string { return newUUIDHex() }
