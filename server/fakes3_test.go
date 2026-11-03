package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeS3 is the least an object store has to be for the server: HEAD on the bucket (the start-up and the readiness check), and PUT, GET and
// DELETE of an object, kept in memory. down makes every answer 503.
type fakeS3 struct {
	*httptest.Server
	down    atomic.Bool
	heads   atomic.Int64
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3(t testing.TB) *fakeS3 {
	f := &fakeS3{objects: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		path := strings.TrimPrefix(req.URL.Path, "/")
		_, key, hasKey := strings.Cut(path, "/")
		switch {
		case req.Method == http.MethodHead && !hasKey:
			f.heads.Add(1)
			w.WriteHeader(http.StatusOK)
		case req.Method == http.MethodPut && hasKey:
			b, _ := io.ReadAll(req.Body)
			f.mu.Lock()
			f.objects[key] = b
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case req.Method == http.MethodGet && hasKey:
			f.mu.Lock()
			b, ok := f.objects[key]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case req.Method == http.MethodDelete && hasKey:
			f.mu.Lock()
			delete(f.objects, key)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *fakeS3) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.objects) }
