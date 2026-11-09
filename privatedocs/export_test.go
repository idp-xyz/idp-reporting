package privatedocs

// OpenFileBackendForTests opens the private directory backend for the contract test.
func OpenFileBackendForTests(dir string) (Backend, error) { return openFileBackend(dir) }
