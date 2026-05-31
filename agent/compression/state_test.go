package compression

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

func TestShouldCompress_Disabled(t *testing.T) {
	cfg := DefaultCompressionConfig()
	cfg.Enabled = false
	c := NewDefaultCompressor(cfg, &mockModel{})
	if c.ShouldCompress(99999) {
		t.Fatal("should not compress when disabled")
	}
}

func TestShouldCompress_Cooldown(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), &mockModel{})
	c.UpdateModel("gpt-4", 8192)
	c.enterCooldown(fmt.Errorf("test error"))
	if c.ShouldCompress(99999) {
		t.Fatal("should not compress during cooldown")
	}
}

func TestShouldCompress_AntiThrash(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), &mockModel{})
	c.UpdateModel("gpt-4", 8192)
	c.state.ineffectiveCount = 999
	if c.ShouldCompress(99999) {
		t.Fatal("should not compress when anti-thrash triggered")
	}
}

func TestRecordCompressionResult(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	c.recordCompressionResult(1000, 800) // 20% savings
	if c.state.ineffectiveCount != 0 {
		t.Fatalf("expected ineffectiveCount 0, got %d", c.state.ineffectiveCount)
	}
	c.recordCompressionResult(1000, 950) // 5% savings
	if c.state.ineffectiveCount != 1 {
		t.Fatalf("expected ineffectiveCount 1, got %d", c.state.ineffectiveCount)
	}
}

func TestEnterCooldown(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	c.enterCooldown(fmt.Errorf("boom"))
	if !c.isInCooldown() {
		t.Fatal("expected to be in cooldown")
	}
	c.state.summaryCooldownUntil = time.Now().Add(-1 * time.Second)
	if c.isInCooldown() {
		t.Fatal("expected cooldown to have expired")
	}
}

func TestBuildStaticFallbackSummary(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("do something")},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
	}
	summary := c.buildStaticFallbackSummary(msgs)
	if !strings.Contains(summary, "do something") {
		t.Fatalf("expected user message in summary, got:\n%s", summary)
	}
	if !strings.Contains(summary, "read") {
		t.Fatalf("expected tool call in summary, got:\n%s", summary)
	}
}
