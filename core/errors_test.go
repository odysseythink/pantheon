package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestProviderError_Error(t *testing.T) {
	err := &ProviderError{Message: "something went wrong", Code: "E001", Status: 400}
	if err.Error() != "something went wrong" {
		t.Errorf("unexpected error message: %q", err.Error())
	}
}

func TestProviderError_IsRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{429, true},
		{408, true},
		{409, true},
		{500, true},
		{502, true},
		{503, true},
		{504, true},
		{400, false},
		{401, false},
		{403, false},
		{413, false},
		{200, false},
		{0, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d", tt.status), func(t *testing.T) {
			err := &ProviderError{Status: tt.status}
			if got := err.IsRetryable(); got != tt.want {
				t.Errorf("IsRetryable() for status %d = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestProviderError_IsContextTooLong(t *testing.T) {
	tests := []struct {
		status  int
		message string
		want    bool
	}{
		{413, "", true},
		{400, "context window exceeded", true},
		{400, "too many tokens", true},
		{400, "message length too long", true},
		{400, "input too long", true},
		{400, "bad request", false},
		{400, "", false},
		{200, "", false},
		{500, "context error", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("status_%d_msg_%s", tt.status, tt.message), func(t *testing.T) {
			err := &ProviderError{Status: tt.status, Message: tt.message}
			if got := err.IsContextTooLong(); got != tt.want {
				t.Errorf("IsContextTooLong() for status %d, msg %q = %v, want %v", tt.status, tt.message, got, tt.want)
			}
		})
	}
}

func TestSentinelErrors(t *testing.T) {
	if !errors.Is(ErrNoObjectGenerated, ErrNoObjectGenerated) {
		t.Error("ErrNoObjectGenerated should match itself")
	}
	if !errors.Is(ErrModelNotFound, ErrModelNotFound) {
		t.Error("ErrModelNotFound should match itself")
	}
	if !errors.Is(ErrUnsupportedFeature, ErrUnsupportedFeature) {
		t.Error("ErrUnsupportedFeature should match itself")
	}
}
