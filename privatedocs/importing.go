package privatedocs

import (
	"encoding/json"
	"path/filepath"

	r "idp.local/reporting/reporting"
)

// OpenDirectory opens a private directory as a backend, for the import of its documents into another backend (S11-C). It takes the
// directory's lock like Open: the server that uses the directory must be stopped.
func OpenDirectory(dir string) (Backend, error) { return openFileBackend(dir) }

// CheckDocument holds a document to the rules every store's document follows (the records, their versions, releases and catalog), without
// the store's own validation of each version: for the backgrounds and the images, whose stores' validators are their package's.
func CheckDocument(data []byte, cap int) error {
	if cap < 1 || cap > 1024 {
		return r.ErrInvalid
	}
	s := &Store[json.RawMessage]{validate: func(json.RawMessage) error { return nil }, binding: func(json.RawMessage) string { return "" }, cap: cap}
	_, e := s.decode(data)
	return e
}

// DirectoryFileName is the name of an owner's file in a private directory: the import matches the directory's files to the identities of the
// bindings by it (a file names no identity, only its digest).
func DirectoryFileName(owner r.Identity) string { return filepath.Base((&fileBackend{}).path(owner)) }
