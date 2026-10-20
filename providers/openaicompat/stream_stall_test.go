package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

// 跨网段静默断流复盘（2026-10-20 odyBox 挂死案）：http.DefaultClient 无任何
// 超时，流式连接被中间设备丢弃后两端都自认 ESTABLISHED，agent 轮次永久阻塞在
// 流读取上。看门狗要求：任何字节（含响应头等待阶段）超过 StreamIdleTimeout
// 未到达即中止流并返回 ErrStreamStalled，让轮次以可见错误收尾。

func streamUserRequest() *core.Request {
	return &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}
}

// 响应头阶段就卡住：服务器 accept 后一个字节都不写。
func TestChatCompletionStream_StallBeforeHeaders(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 永不返回响应
	}))
	defer server.Close()
	defer close(release) // LIFO：先放 handler 走，server.Close() 才不会等死

	c := NewClient(server.URL, "sk-test")
	c.StreamIdleTimeout = 50 * time.Millisecond

	start := time.Now()
	var gotErr error
	for _, err := range c.ChatCompletionStream(context.Background(), "gpt-4", streamUserRequest()) {
		gotErr = err
	}
	elapsed := time.Since(start)

	if !errors.Is(gotErr, ErrStreamStalled) {
		t.Fatalf("expected ErrStreamStalled, got %v", gotErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("watchdog took too long: %s", elapsed)
	}
}

// 流中途卡住：服务器发了一个 chunk 后永久沉默。
func TestChatCompletionStream_StallMidStream(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n")
		flusher.Flush()
		<-release // 第一个 chunk 之后永久沉默
	}))
	defer server.Close()
	defer close(release) // LIFO：先放 handler 走，server.Close() 才不会等死

	c := NewClient(server.URL, "sk-test")
	c.StreamIdleTimeout = 50 * time.Millisecond

	var texts []string
	var gotErr error
	for part, err := range c.ChatCompletionStream(context.Background(), "gpt-4", streamUserRequest()) {
		if err != nil {
			gotErr = err
			continue
		}
		if part != nil && part.Type == core.StreamPartTypeTextDelta {
			texts = append(texts, part.TextDelta)
		}
	}

	if len(texts) != 1 || texts[0] != "Hello" {
		t.Fatalf("expected the delivered chunk before the stall, got %v", texts)
	}
	if !errors.Is(gotErr, ErrStreamStalled) {
		t.Fatalf("expected ErrStreamStalled, got %v", gotErr)
	}
}

// 慢但持续有数据的流不能误杀：chunk 间隔低于空闲上限时必须正常完成。
func TestChatCompletionStream_SlowButAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunks := []string{"Hello", " world"}
		for _, text := range chunks {
			fmt.Fprintf(w, "data: {\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
			flusher.Flush()
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"model\":\"gpt-4\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.StreamIdleTimeout = 300 * time.Millisecond

	var texts []string
	var gotErr error
	for part, err := range c.ChatCompletionStream(context.Background(), "gpt-4", streamUserRequest()) {
		if err != nil {
			gotErr = err
			continue
		}
		if part != nil && part.Type == core.StreamPartTypeTextDelta {
			texts = append(texts, part.TextDelta)
		}
	}

	if gotErr != nil {
		t.Fatalf("slow-but-alive stream must not be killed, got %v", gotErr)
	}
	if len(texts) != 2 {
		t.Fatalf("expected 2 text deltas, got %v", texts)
	}
}
