package objects

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	r "idp.local/reporting/reporting"
)

// The operator's configuration of the object store (S04-E, docs/S04E_SCOPE.md E5, docs/OPERATIONS.md "Object storage"): the endpoint, the bucket,
// the region, the CA file and the loopback-http option are flags; the access key and the secret key are the environment's alone, and a flag for
// either is refused, so that neither lands in a process list, a shell history or a unit file. The demo and `reporting-migrate sweep` share this.

// The environment variables of the credentials.
const (
	AccessKeyEnv = "REPORTING_OBJECT_STORE_ACCESS_KEY"
	SecretKeyEnv = "REPORTING_OBJECT_STORE_SECRET_KEY"
)

// Flags are the object store flags of a command.
type Flags struct {
	Endpoint, Bucket, Region, CA string
	LoopbackHTTP                 bool
}

// Register adds the object store flags to the set: -object-store-endpoint, -object-store-bucket, -object-store-region (default us-east-1),
// -object-store-ca and -object-store-loopback-http.
func Register(fs *flag.FlagSet) *Flags {
	f := &Flags{}
	fs.StringVar(&f.Endpoint, "object-store-endpoint", "", "S3-compatible object store for artifact files (S04-E): https://host[:port], or http://127.0.0.1:port with -object-store-loopback-http; the keys are in "+AccessKeyEnv+" and "+SecretKeyEnv)
	fs.StringVar(&f.Bucket, "object-store-bucket", "", "the bucket the artifact files live in (it must exist; the platform never creates one)")
	fs.StringVar(&f.Region, "object-store-region", "us-east-1", "the bucket's region, for request signing")
	fs.StringVar(&f.CA, "object-store-ca", "", "optional PEM file of the CA that vouches for an https object store (it replaces the system roots for that store)")
	fs.BoolVar(&f.LoopbackHTTP, "object-store-loopback-http", false, "allow an http:// object store on a loopback IP address, for the demo and the qualification")
	return f
}

// Set says whether any object store flag was given a value.
func (f *Flags) Set() bool {
	return f.Endpoint != "" || f.Bucket != "" || f.CA != "" || f.LoopbackHTTP
}

// RefuseCredentialFlags fails if the arguments carry a flag for the access key or the secret key, whatever its spelling. It reads only the names:
// the refusal does not repeat what follows them (the flag package would print the value of a flag it refuses).
func RefuseCredentialFlags(args []string) error {
	for _, a := range args {
		if a == "--" {
			return nil
		}
		name := strings.TrimLeft(a, "-")
		if len(name) == len(a) {
			continue
		}
		name, _, _ = strings.Cut(name, "=")
		switch name {
		case "object-store-access-key", "object-store-secret-key", "object-store-key", "object-store-secret", "object-store-access-key-id", "object-store-secret-access-key":
			return fmt.Errorf("the object store's keys are never flags: set %s and %s in the environment: %w", AccessKeyEnv, SecretKeyEnv, r.ErrInvalid)
		}
	}
	return nil
}

// Client makes the client the flags and the environment name, or nil and no error when no flag was given (the store is not configured). With
// any flag given the endpoint and the bucket are required, and so are both keys in the environment: a refusal names the missing piece, never a
// value. The caller then calls Open to reach the bucket.
func (f *Flags) Client(getenv func(string) string) (*S3, error) {
	if !f.Set() {
		return nil, nil
	}
	if f.Endpoint == "" || f.Bucket == "" {
		return nil, fmt.Errorf("the object store needs -object-store-endpoint and -object-store-bucket together: %w", r.ErrInvalid)
	}
	access, secret := getenv(AccessKeyEnv), getenv(SecretKeyEnv)
	if access == "" || secret == "" {
		return nil, fmt.Errorf("the object store's keys are read from %s and %s, which must both be set: %w", AccessKeyEnv, SecretKeyEnv, r.ErrInvalid)
	}
	return NewS3(S3{Endpoint: f.Endpoint, Bucket: f.Bucket, Region: f.Region, AccessKey: access, SecretKey: secret, CA: f.CA, LoopbackHTTP: f.LoopbackHTTP})
}

// StartupTimeout bounds the check at startup, a retry of it included.
const StartupTimeout = 3 * RequestTimeout

// CheckAtStart is HEAD bucket, and fails with a text that names the bucket and the status and nothing else: not the endpoint, not a credential.
func CheckAtStart(ctx context.Context, s *S3) error {
	ctx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	err := s.Check(ctx)
	if err == nil {
		return nil
	}
	var o *r.ObjectStoreError
	if errors.As(err, &o) && o.Status != 0 {
		return fmt.Errorf("the object store's bucket %q is not usable: HEAD bucket answered status %d", s.Bucket, o.Status)
	}
	return fmt.Errorf("the object store's bucket %q is not usable: HEAD bucket got no answer", s.Bucket)
}
