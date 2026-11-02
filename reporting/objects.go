package reporting

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Object storage for artifacts (S04-E, docs/ADR_0007_OBJECT_STORAGE.md, docs/S04E_SCOPE.md). With an object store configured the content of a
// new artifact is an object and its row carries the metadata, the object's key and the SHA-256 and length the object must match. The
// implementations are storage/objects.S3 (any S3-compatible service) and storage/objects.Memory (tests); this package owns only the contract.

// ObjectPrefix is where artifact objects live in the bucket; the key of an artifact is ObjectPrefix plus its id (ids are random, so a key says
// nothing).
const ObjectPrefix = "artifacts/"

// ObjectKey is the key of the object that holds the content of the artifact with the id.
func ObjectKey(artifactID string) string { return ObjectPrefix + artifactID }

// ObjectID is the artifact id a key names: the key must be exactly ObjectKey of a canonical id, anything else is not ours.
func ObjectID(key string) (string, bool) {
	id, ok := strings.CutPrefix(key, ObjectPrefix)
	return id, ok && validID(id)
}

// Object is one entry of a listing: the key and the time the object store stamped on it.
type Object struct {
	Key          string
	LastModified time.Time
}

// ObjectStore is the object store the platform writes artifact content to and reads it back from. Every method is safe for concurrent use.
//
//   - Put stores the content under the key; sha256 is the lowercase hex digest of content, which the store sends so that a body altered on its
//     way is refused. Putting a key again replaces it.
//   - Get returns the content under the key, which must be exactly length bytes with the digest sha256, else ErrIntegrity (a missing object
//     too: the row promises it). It never reads more than MaxArtifactBytes. A store that cannot be reached, or answers what it should not, is
//     an *ObjectStoreError (ErrUnavailable).
//   - Delete removes the object; deleting an object that is not there is not an error.
//   - List lists the objects under the prefix in key order, at most limit of them (1..1000), starting at the continuation token after ("" for
//     the first page), and returns the token of the next page ("" when there is none).
//   - Check says whether the store is usable: the bucket exists and the credentials may reach it.
//
// No error carries a response body, a key, an endpoint or a credential.
type ObjectStore interface {
	Put(ctx context.Context, key string, content []byte, sha256 string) error
	Get(ctx context.Context, key, sha256 string, length int) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix, after string, limit int) ([]Object, string, error)
	Check(ctx context.Context) error
}

// ObjectStoreError is ErrUnavailable from the object store: the store could not be reached, or answered what the platform does not accept. It
// carries the HTTP status when there was an answer (0 when there was none) and nothing else: no body, no key, no endpoint, no credential. The
// HTTP API answers it 503 with a Retry-After.
type ObjectStoreError struct{ Status int }

func (e *ObjectStoreError) Error() string {
	if e.Status == 0 {
		return "object store unavailable"
	}
	return fmt.Sprintf("object store unavailable (status %d)", e.Status)
}
func (e *ObjectStoreError) Is(target error) bool { return target == ErrUnavailable }

// IsObjectStoreError says whether an error is the object store's (ObjectStoreError), as opposed to the source product's ErrUnavailable.
func IsObjectStoreError(e error) bool {
	var o *ObjectStoreError
	return errors.As(e, &o)
}

// errObjectStored is what a job whose artifact could not be put in the object store fails with: an error of no known kind, so the job's code
// is EXECUTION_FAILED, and nothing of the store in it.
var errObjectStored = errors.New("the artifact could not be stored")

func validSHA256(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}

// ValidArtifactBody says whether an artifact carries its content in exactly one of the two ways (S04-E): inline, with Content whose length is
// within bounds and whose digest is SHA256, and no Object or Size; or in the object store, with Object the key of its own id, a Size within
// bounds, a digest, and no Content. Both or neither is not valid. Where the bytes are is the row's business; whether they are the row's
// bytes is checked when they are read.
func ValidArtifactBody(a Artifact) bool {
	if a.Object == "" {
		return a.Size == 0 && len(a.Content) > 0 && len(a.Content) <= MaxArtifactBytes && a.SHA256 == bytesDigest(a.Content)
	}
	return len(a.Content) == 0 && a.Object == ObjectKey(a.ID) && a.Size > 0 && a.Size <= MaxArtifactBytes && validSHA256(a.SHA256)
}

// BytesDigestForTests is the SHA-256 (hex) the store keeps of a file, for the qualification tools that put objects and rows of their own.
func BytesDigestForTests(b []byte) string { return bytesDigest(b) }
