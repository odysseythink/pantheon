package openaicompat

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// ErrStreamStalled marks a streaming response that went silent beyond
// StreamIdleTimeout: the connection looks established at TCP level but no
// bytes arrive (a stateful middlebox dropped the flow, upstream hung, ...).
// Failing the stream lets the caller surface a visible error instead of
// blocking the agent turn forever (2026-10-20 odyBox 跨网段断流挂死案).
var ErrStreamStalled = errors.New("openaicompat: stream stalled (no data within idle timeout)")

// stallWatchdog wraps a response body and closes it when no bytes arrive for
// longer than the idle timeout. Activity is tracked on every successful Read,
// so slow-but-progressing streams are never killed.
type stallWatchdog struct {
	body       io.ReadCloser
	idle       time.Duration
	lastActive atomic.Int64 // unix nano of last successful Read
	expired    atomic.Bool
	done       chan struct{}
	closeOnce  sync.Once
}

func newStallWatchdog(body io.ReadCloser, idle time.Duration) *stallWatchdog {
	w := &stallWatchdog{body: body, idle: idle, done: make(chan struct{})}
	w.lastActive.Store(time.Now().UnixNano())
	tick := idle / 4
	if tick > time.Second {
		tick = time.Second
	}
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-w.done:
				return
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, w.lastActive.Load())) > w.idle {
					w.expired.Store(true)
					// Closing the body unblocks the pending Read with an
					// error; the caller checks expired to report
					// ErrStreamStalled instead of the raw close error.
					_ = w.body.Close()
					return
				}
			}
		}
	}()
	return w
}

func (w *stallWatchdog) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.lastActive.Store(time.Now().UnixNano())
	}
	return n, err
}

func (w *stallWatchdog) Close() error {
	w.closeOnce.Do(func() { close(w.done) })
	return w.body.Close()
}
