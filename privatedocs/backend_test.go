package privatedocs_test

import (
	"path/filepath"
	"testing"

	"idp.local/reporting/privatedocs"
	"idp.local/reporting/privatedocs/backendtest"
)

func TestMemoryBackendKeepsTheContract(t *testing.T) {
	backendtest.Run(t, func(*testing.T) privatedocs.Backend { return privatedocs.NewMemoryBackend() }, backendtest.Options{Serializes: true})
}

// The file backend is reached through Open (its type is the package's): the store's own lock serializes its changes.
func TestFileBackendKeepsTheContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) privatedocs.Backend {
		b, e := privatedocs.OpenFileBackendForTests(filepath.Join(t.TempDir(), "d"))
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}, backendtest.Options{})
}
