package core

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
)

// ObjectResult is a typed wrapper around ObjectResponse.
type ObjectResult[T any] struct {
	Object       T
	RawText      string
	Usage        Usage
	FinishReason string
	Model        string
	Warnings     []CallWarning
}

// StreamObjectResult provides typed access to a streaming object generation result.
type StreamObjectResult[T any] struct {
	stream ObjectStreamResponse
	ctx    context.Context
}

// NewStreamObjectResult creates a typed stream result from an untyped stream.
func NewStreamObjectResult[T any](ctx context.Context, stream ObjectStreamResponse) *StreamObjectResult[T] {
	return &StreamObjectResult[T]{
		stream: stream,
		ctx:    ctx,
	}
}

// PartialObjectStream returns an iterator that yields progressively more complete objects.
// Only emits when the object actually changes (deduplication).
func (s *StreamObjectResult[T]) PartialObjectStream() iter.Seq[T] {
	return func(yield func(T) bool) {
		var lastObject T
		var hasEmitted bool

		for part, err := range s.stream {
			if err != nil {
				return
			}
			if part.Type != ObjectStreamPartTypeObject || part.Object == nil {
				continue
			}
			var current T
			if err := unmarshalObjectPart(part.Object, &current); err != nil {
				continue
			}
			if !hasEmitted || !reflect.DeepEqual(current, lastObject) {
				if !yield(current) {
					return
				}
				lastObject = current
				hasEmitted = true
			}
		}
	}
}

// TextStream returns an iterator that yields text deltas.
// Useful if the model generates explanatory text alongside the object.
func (s *StreamObjectResult[T]) TextStream() iter.Seq[string] {
	return func(yield func(string) bool) {
		for part, err := range s.stream {
			if err != nil {
				return
			}
			if part.Type == ObjectStreamPartTypeTextDelta && part.TextDelta != "" {
				if !yield(part.TextDelta) {
					return
				}
			}
		}
	}
}

// FullStream returns the underlying raw stream.
func (s *StreamObjectResult[T]) FullStream() ObjectStreamResponse {
	return s.stream
}

// Object waits for the stream to complete and returns the final typed object.
// Returns an error if streaming fails or no valid object was generated.
func (s *StreamObjectResult[T]) Object() (*ObjectResult[T], error) {
	var finalObject T
	var rawText string
	var usage Usage
	var finishReason string
	var warnings []CallWarning
	var hasObject bool
	var lastErr error

	for part, err := range s.stream {
		if err != nil {
			lastErr = err
			continue
		}
		switch part.Type {
		case ObjectStreamPartTypeObject:
			if part.Object != nil {
				if err := unmarshalObjectPart(part.Object, &finalObject); err == nil {
					hasObject = true
					if b, err := json.Marshal(part.Object); err == nil {
						rawText = string(b)
					}
				}
			}
		case ObjectStreamPartTypeFinish:
			if part.Usage != nil {
				usage = *part.Usage
			}
			finishReason = part.FinishReason
			if len(part.Warnings) > 0 {
				warnings = append(warnings, part.Warnings...)
			}
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if !hasObject {
		return nil, fmt.Errorf("no valid object generated in stream")
	}

	return &ObjectResult[T]{
		Object:       finalObject,
		RawText:      rawText,
		Usage:        usage,
		FinishReason: finishReason,
		Warnings:     warnings,
	}, nil
}

func unmarshalObjectPart(obj map[string]any, target any) error {
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
