package core

import "errors"

type ProviderError struct {
	Message string
	Code    string
	Status  int
}

func (e *ProviderError) Error() string {
	return e.Message
}

func (e *ProviderError) IsRetryable() bool {
	if e.Status == 429 || e.Status == 408 || e.Status == 409 {
		return true
	}
	if e.Status >= 500 {
		return true
	}
	return false
}

func (e *ProviderError) IsContextTooLong() bool {
	return e.Status == 413 || (e.Status == 400 && len(e.Message) > 100)
}

var ErrNoObjectGenerated = errors.New("no object generated")
var ErrModelNotFound = errors.New("model not found")
var ErrUnsupportedFeature = errors.New("unsupported feature")
