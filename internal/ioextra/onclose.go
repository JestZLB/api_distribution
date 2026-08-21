// Package ioextra provides small io-related utilities reused across
// the project.
package ioextra

import (
	"io"
	"sync"
)

// WrapCloser wraps rc so that onClose runs exactly once when the body
// is closed. Concurrent Close calls are safe: both the underlying
// Close and onClose are invoked at most once. If the underlying Close
// panics, onClose still runs because it sits in a defer.
//
// onClose itself is responsible for handling any panic it may raise;
// this wrapper does not swallow panics from either side.
//
// Typical use case: pair an HTTP response body with a reference-count
// release in a connection-pool cache, so the cache entry can be
// reaped once the response body has been fully consumed.
func WrapCloser(rc io.ReadCloser, onClose func()) io.ReadCloser {
	return &onCloserBody{ReadCloser: rc, onClose: onClose}
}

type onCloserBody struct {
	io.ReadCloser
	onClose func()
	once    sync.Once
}

func (b *onCloserBody) Close() error {
	var err error
	b.once.Do(func() {
		defer b.onClose()
		err = b.ReadCloser.Close()
	})
	return err
}
