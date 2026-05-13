package catwalk

import "errors"

var (
	ErrCatwalkUnavailable   = errors.New("catwalk service unavailable")
	ErrProviderNotFound     = errors.New("provider not found in catwalk")
	ErrProviderNotSupported = errors.New("provider does not support listing models via API")
)
