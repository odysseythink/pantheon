package compression

import (
	"context"
	"errors"
	"fmt"
	"iter"
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

func TestAccessors(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	if c.PreviousSummary() != "" {
		t.Fatalf("expected empty previousSummary, got %q", c.PreviousSummary())
	}
	c.SetPreviousSummary("hello world")
	if c.PreviousSummary() != "hello world" {
		t.Fatalf("expected previousSummary=hello world, got %q", c.PreviousSummary())
	}
	if c.LastFallbackUsed() {
		t.Fatal("expected LastFallbackUsed=false")
	}
}

func TestCooldownThirdTier(t *testing.T) {
	cfg := DefaultCompressionConfig()
	cfg.CooldownEnabled = true
	cfg.CooldownBase = 30 * time.Second
	cfg.CooldownMax = 60 * time.Second
	c := NewDefaultCompressor(cfg, nil)
	c.state.ineffectiveCount = 5
	c.enterCooldown(nil)
	if time.Now().After(c.state.summaryCooldownUntil) {
		t.Fatal("expected cooldown to be active")
	}
	remaining := time.Until(c.state.summaryCooldownUntil)
	if remaining < 590*time.Second || remaining > 610*time.Second {
		t.Fatalf("expected ~600s cooldown, got %v", remaining)
	}
}

func TestFallbackModelRetry(t *testing.T) {
	// This test uses a mock LanguageModel that fails on first call and succeeds on second.
	primary := &mockFailingLM{failCount: 1}
	fallback := &mockFailingLM{failCount: 0}
	cfg := DefaultCompressionConfig()
	cfg.FallbackModel = "fallback-model"
	c := NewDefaultCompressor(cfg, primary)
	c.SetFallbackModel(fallback)

	msgs := []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_USER, "hello")}
	summary, err := c.generateSummaryWithFallback(context.Background(), msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !c.LastFallbackUsed() {
		t.Fatal("expected LastFallbackUsed=true")
	}
}

type mockFailingLM struct {
	failCount int
	calls     int
}

func (m *mockFailingLM) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	if m.failCount > 0 {
		m.failCount--
		return nil, errors.New("primary failure")
	}
	return &core.Response{Message: core.NewTextMessage(core.MESSAGE_ROLE_ASSISTANT, "fallback summary")}, nil
}

func (m *mockFailingLM) Stream(ctx context.Context, req *core.Request) (iter.Seq2[*core.StreamPart, error], error) {
	return nil, errors.New("not implemented")
}

func (m *mockFailingLM) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (m *mockFailingLM) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}

func (m *mockFailingLM) Provider() string { return "mock-failing" }
func (m *mockFailingLM) Model() string    { return "mock-failing-model" }
