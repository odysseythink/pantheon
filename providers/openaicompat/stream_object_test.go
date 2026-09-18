package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestStreamObject_TextMode(t *testing.T) {
	var chunks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		// Stream a partial JSON object in text deltas
		chunks = []string{
			`{"name": "A`,
			`lice", "age": `,
			`30}`,
		}

		for _, text := range chunks {
			data, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{
					{
						"delta": map[string]any{
							"content": text,
						},
					},
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		// Finish chunk
		final, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"delta":         map[string]string{},
					"finish_reason": "stop",
				},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", final)
		flusher.Flush()

		// Usage chunk (OpenAI sends this separately when stream_options.include_usage=true)
		usageData, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{},
			"usage": map[string]int{
				"prompt_tokens":     5,
				"completion_tokens": 3,
				"total_tokens":      8,
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", usageData)
		flusher.Flush()
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := NewClient(server.URL, "sk-test")
	stream := client.StreamObject(context.Background(), "gpt-4", &core.ObjectRequest{
		Mode:   core.ObjectModeJSON,
		Schema: &core.Schema{Type: "object"},
	})

	var objects []map[string]any
	var finishPart *core.ObjectStreamPart

	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.ObjectStreamPartTypeObject:
			objects = append(objects, part.Object)
		case core.ObjectStreamPartTypeFinish:
			finishPart = part
		}
	}

	if len(objects) == 0 {
		t.Fatal("expected at least one object part")
	}

	last := objects[len(objects)-1]
	if last["name"] != "Alice" || last["age"] != float64(30) {
		t.Errorf("unexpected final object: %+v", last)
	}

	if finishPart == nil {
		t.Fatal("expected finish part")
	}
	if finishPart.FinishReason != "stop" {
		t.Errorf("expected finish reason stop, got %q", finishPart.FinishReason)
	}
	if finishPart.Usage == nil || finishPart.Usage.TotalTokens != 8 {
		t.Errorf("unexpected usage: %+v", finishPart.Usage)
	}
}

func TestStreamObject_NoValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		data, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"delta": map[string]any{
						"content": "not json at all",
					},
				},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		final, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"delta":         map[string]string{},
					"finish_reason": "stop",
				},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", final)
		flusher.Flush()
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := NewClient(server.URL, "sk-test")
	stream := client.StreamObject(context.Background(), "gpt-4", &core.ObjectRequest{
		Mode:   core.ObjectModeJSON,
		Schema: &core.Schema{Type: "object"},
	})

	var objects []map[string]any
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.ObjectStreamPartTypeObject {
			objects = append(objects, part.Object)
		}
	}

	if len(objects) != 0 {
		t.Errorf("expected 0 objects for invalid JSON, got %d", len(objects))
	}
}

func TestStreamObject_ObjectStreamResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		data, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"delta": map[string]any{
						"content": `{"city":"NYC"}`,
					},
				},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()

		final, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"delta":         map[string]string{},
					"finish_reason": "stop",
				},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", final)
		flusher.Flush()

		usageData, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{},
			"usage": map[string]int{
				"prompt_tokens":     2,
				"completion_tokens": 3,
				"total_tokens":      5,
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", usageData)
		flusher.Flush()
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := NewClient(server.URL, "sk-test")
	stream := client.StreamObject(context.Background(), "gpt-4", &core.ObjectRequest{
		Mode:   core.ObjectModeJSON,
		Schema: &core.Schema{Type: "object"},
	})

	result, err := ObjectStreamResult(stream)
	if err != nil {
		t.Fatalf("ObjectStreamResult error: %v", err)
	}

	if result.Object["city"] != "NYC" {
		t.Errorf("unexpected object: %+v", result.Object)
	}
	if result.FinishReason != "stop" {
		t.Errorf("unexpected finish reason: %q", result.FinishReason)
	}
	if result.Usage.TotalTokens != 5 {
		t.Errorf("unexpected usage: %+v", result.Usage)
	}
}

func TestMapsEqual(t *testing.T) {
	if !mapsEqual(nil, nil) {
		t.Error("nil maps should be equal")
	}
	if mapsEqual(nil, map[string]any{"a": 1}) {
		t.Error("nil vs non-nil should not be equal")
	}
	if !mapsEqual(map[string]any{"a": 1}, map[string]any{"a": 1}) {
		t.Error("identical maps should be equal")
	}
	if mapsEqual(map[string]any{"a": 1}, map[string]any{"a": 2}) {
		t.Error("different maps should not be equal")
	}
}
