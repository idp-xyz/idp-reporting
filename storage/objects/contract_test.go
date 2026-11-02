package objects

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

// runStoreContract is what every reporting.ObjectStore does the same way, run on Memory and, against a throwaway server, on S3. Every key it
// writes is under prefix, so that two runs, or a server shared with other tests, never meet.
func runStoreContract(t *testing.T, s r.ObjectStore, prefix string) {
	ctx := context.Background()
	content := []byte("%PDF-1.7 the content of an artifact")
	sum := digest(content)
	key := prefix + "one"
	t.Cleanup(func() {
		for _, k := range []string{key, prefix + "two", prefix + "three", prefix + "four", prefix + "five"} {
			_ = s.Delete(ctx, k)
		}
	})
	t.Run("check", func(t *testing.T) {
		if err := s.Check(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("put_get_and_replace", func(t *testing.T) {
		if err := s.Put(ctx, key, content, sum); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, key, sum, len(content))
		if err != nil || string(got) != string(content) {
			t.Fatal(got, err)
		}
		other := []byte("another file")
		if err := s.Put(ctx, key, other, digest(other)); err != nil {
			t.Fatal(err)
		}
		if got, err = s.Get(ctx, key, digest(other), len(other)); err != nil || string(got) != string(other) {
			t.Fatal(got, err)
		}
		// The old row no longer matches the replaced object.
		if _, err = s.Get(ctx, key, sum, len(content)); !errors.Is(err, r.ErrIntegrity) {
			t.Fatal(err)
		}
		if err := s.Put(ctx, key, content, sum); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("get_refuses_what_the_row_does_not_describe", func(t *testing.T) {
		for name, c := range map[string]struct {
			key, sum string
			length   int
		}{
			"another length": {key, sum, len(content) - 1},
			"a longer row":   {key, sum, len(content) + 1},
			"another digest": {key, digest([]byte("x")), len(content)},
			"missing":        {prefix + "nothing-here", sum, len(content)},
		} {
			got, err := s.Get(ctx, c.key, c.sum, c.length)
			if !errors.Is(err, r.ErrIntegrity) || got != nil {
				t.Errorf("%s: %v %q", name, err, got)
			}
			if err != nil && (strings.Contains(err.Error(), c.key) || strings.Contains(err.Error(), prefix)) {
				t.Errorf("%s: the error names the key: %q", name, err)
			}
		}
	})
	t.Run("delete_is_idempotent", func(t *testing.T) {
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal("deleting what is not there:", err)
		}
		if _, err := s.Get(ctx, key, sum, len(content)); !errors.Is(err, r.ErrIntegrity) {
			t.Fatal(err)
		}
	})
	t.Run("list_pages_by_prefix_in_key_order", func(t *testing.T) {
		names := []string{"five", "two", "three", "four", "one"}
		for _, n := range names {
			if err := s.Put(ctx, prefix+n, []byte(n), digest([]byte(n))); err != nil {
				t.Fatal(err)
			}
		}
		// An object of another prefix is never listed.
		var all []string
		after := ""
		for pages := 0; ; pages++ {
			objs, next, err := s.List(ctx, prefix, after, 2)
			if err != nil || len(objs) > 2 || pages > 5 {
				t.Fatal(objs, next, err, pages)
			}
			for _, o := range objs {
				if o.LastModified.IsZero() || time.Since(o.LastModified) > time.Hour || time.Until(o.LastModified) > time.Hour {
					t.Errorf("an object put now is stamped %s", o.LastModified)
				}
				all = append(all, o.Key)
			}
			if next == "" {
				break
			}
			if len(objs) != 2 {
				t.Fatalf("a page of %d with more to come", len(objs))
			}
			after = next
		}
		want := []string{prefix + "five", prefix + "four", prefix + "one", prefix + "three", prefix + "two"}
		if !slices.Equal(all, want) {
			t.Fatalf("listed %v, want %v", all, want)
		}
		if objs, next, err := s.List(ctx, prefix+"t", "", 10); err != nil || next != "" || len(objs) != 2 || objs[0].Key != prefix+"three" || objs[1].Key != prefix+"two" {
			t.Fatal(objs, next, err)
		}
		if objs, _, err := s.List(ctx, prefix+"zzz", "", 10); err != nil || len(objs) != 0 {
			t.Fatal(objs, err)
		}
	})
}

func TestMemoryObjectStoreContract(t *testing.T) {
	m := NewMemory(nil)
	t.Run("contract", func(t *testing.T) { runStoreContract(t, m, "artifacts/contract-") })
	if len(m.Keys()) != 0 {
		t.Errorf("the contract left %v", m.Keys())
	}
}

func TestMemoryObjectStoreIsStampedByItsOwnClockAndBreaksOnRequest(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	m := NewMemory(func() time.Time { return clock })
	b := []byte("abc")
	if err := m.Put(ctx, "artifacts/a", b, digest(b)); err != nil {
		t.Fatal(err)
	}
	objs, _, _ := m.List(ctx, "artifacts/", "", 10)
	if len(objs) != 1 || !objs[0].LastModified.Equal(clock) {
		t.Fatal(objs)
	}
	m.SetLastModified("artifacts/a", clock.Add(-2*time.Hour))
	objs, _, _ = m.List(ctx, "artifacts/", "", 10)
	if !objs[0].LastModified.Equal(clock.Add(-2 * time.Hour)) {
		t.Fatal(objs)
	}
	// Put refuses a digest that is not the content's, as S3 does.
	if err := m.Put(ctx, "artifacts/b", b, digest([]byte("x"))); !errors.Is(err, r.ErrInvalid) {
		t.Fatal(err)
	}
	m.Replace("artifacts/a", []byte("abd"))
	if _, err := m.Get(ctx, "artifacts/a", digest(b), 3); !errors.Is(err, r.ErrIntegrity) {
		t.Fatal(err)
	}
	m.Remove("artifacts/a")
	if _, err := m.Get(ctx, "artifacts/a", digest(b), 3); !errors.Is(err, r.ErrIntegrity) {
		t.Fatal(err)
	}
	m.Fail(func(op, key string) bool { return op == "put" })
	err := m.Put(ctx, "artifacts/a", b, digest(b))
	var o *r.ObjectStoreError
	if !errors.As(err, &o) || !errors.Is(err, r.ErrUnavailable) || m.Calls("put") != 3 {
		t.Fatal(err, m.Calls("put"))
	}
	m.Fail(nil)
	if err := m.Put(ctx, "artifacts/a", b, digest(b)); err != nil {
		t.Fatal(err)
	}
}

// s3FromEnv is the throwaway server of the qualification: the endpoint and the two keys come from the environment (REPORTING_OBJECT_STORE_TEST_*),
// the bucket is the one the scope names. Without them the test skips, and fails when REPORTING_OBJECT_STORE_REQUIRE=1.
func s3FromEnv(t *testing.T) *S3 {
	t.Helper()
	endpoint, access, secret := os.Getenv("REPORTING_OBJECT_STORE_TEST_ENDPOINT"), os.Getenv("REPORTING_OBJECT_STORE_TEST_ACCESS_KEY"), os.Getenv("REPORTING_OBJECT_STORE_TEST_SECRET_KEY")
	if endpoint == "" || access == "" || secret == "" {
		if os.Getenv("REPORTING_OBJECT_STORE_REQUIRE") == "1" {
			t.Fatal("required object store endpoint and keys (REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY, _SECRET_KEY) missing")
		}
		t.Skip("S3 gate not run: set REPORTING_OBJECT_STORE_TEST_ENDPOINT, _ACCESS_KEY and _SECRET_KEY (a throwaway S3-compatible server)")
	}
	c, err := NewS3(S3{Endpoint: endpoint, Bucket: "reporting-test-s04e", Region: "us-east-1", AccessKey: access, SecretKey: secret, LoopbackHTTP: strings.HasPrefix(endpoint, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := CreateBucketForTests(ctx, c); err != nil {
		t.Fatalf("the test bucket could not be created: %v", err)
	}
	return c
}

// TestS3Objects qualifies the client alone against a throwaway S3-compatible server (MinIO locally, versitygw in CI): the same contract Memory meets, and what only a
// real server can show: that it accepts the signatures, answers the statuses the client expects and refuses what it should.
func TestS3Objects(t *testing.T) {
	c := s3FromEnv(t)
	ctx := context.Background()
	runStoreContract(t, c, fmt.Sprintf("client-test/%d/", time.Now().UnixNano()))
	t.Run("a_body_altered_on_its_way_is_refused_by_the_server", func(t *testing.T) {
		// The server checks x-amz-content-sha256 against the body it received: a Put whose header and body differ is refused by it, which we
		// cannot send through Put (it checks first), so it is sent by hand.
		b := []byte("abc")
		resp, err := c.do(ctx, "PUT", c.objectPath("client-test/altered"), nil, []byte("abd"), digest(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.finish()
		if resp.status != 400 {
			t.Fatalf("the server accepted a body that is not the signed digest: %d", resp.status)
		}
	})
	t.Run("wrong_credentials_and_a_missing_bucket_are_statuses_not_texts", func(t *testing.T) {
		bad := *c
		bad.creds.secret = "not-the-secret-0123456789"
		err := bad.Check(ctx)
		var o *r.ObjectStoreError
		if !errors.As(err, &o) || o.Status != 403 || strings.Contains(err.Error(), "SignatureDoesNotMatch") {
			t.Fatalf("wrong secret: %v", err)
		}
		_, err = bad.Get(ctx, "client-test/x", digest([]byte("x")), 1)
		if !errors.As(err, &o) || o.Status != 403 {
			t.Fatalf("wrong secret, get: %v", err)
		}
		missing := *c
		missing.Bucket = "reporting-test-s04e-absent"
		err = missing.Check(ctx)
		if !errors.As(err, &o) || o.Status != 404 || strings.Contains(err.Error(), missing.Bucket) || strings.Contains(err.Error(), "NoSuchBucket") {
			t.Fatalf("missing bucket: %v", err)
		}
	})
	// S04-H H3, the rotation of the object store's key pair. The throwaway server has the one root key pair and no way to make a second
	// (MinIO could, with its admin API; versitygw in CI is started with a root key only), so a revoked key cannot be shown being revoked. What
	// the server cannot tell apart is shown instead: a request signed by a key it does not accept is answered 403 whether the key was never
	// known, was deleted, or has the wrong secret, so a client built with a wrong key is exactly the running client after a revocation.
	// The procedure (create the new pair, restart with it, delete the old one) is the object store's own and is written down, not proved here.
	t.Run("a_key_the_server_does_not_accept_fails_the_start_and_every_later_request_without_naming_a_key", func(t *testing.T) {
		wrongSecret, unknownKey := *c, *c
		wrongSecret.creds.secret = "rotated-away-secret-0123456789abcdef"
		unknownKey.creds.access = "ROTATEDAWAYACCESSKEY"
		secrets := []string{c.creds.access, c.creds.secret, wrongSecret.creds.secret, unknownKey.creds.access, c.base.Host, c.base.String(), "InvalidAccessKeyId", "SignatureDoesNotMatch", "AccessDenied", "<Error>"}
		noKey := func(what string, err error) {
			t.Helper()
			for _, leak := range secrets {
				if err != nil && strings.Contains(err.Error(), leak) {
					t.Errorf("%s: the error carries %q: %v", what, leak, err)
				}
			}
		}
		// The start: the text names the bucket and the status and nothing else.
		want := fmt.Sprintf("the object store's bucket %q is not usable: HEAD bucket answered status 403", c.Bucket)
		for name, bad := range map[string]*S3{"a wrong secret": &wrongSecret, "an unknown access key": &unknownKey} {
			err := CheckAtStart(ctx, bad)
			if err == nil || err.Error() != want {
				t.Errorf("%s: the start says %v, want %q", name, err, want)
			}
		}
		// A running client whose key stops being accepted: its next request of every kind fails as the store being unavailable, with the
		// status, so that the API answers 503 and Retry-After, and never with the key.
		key := fmt.Sprintf("client-test/rotation-%d", time.Now().UnixNano())
		content := []byte("kept through the rotation")
		if err := c.Put(ctx, key, content, digest(content)); err != nil {
			t.Fatal(err)
		}
		defer c.Delete(ctx, key)
		for name, bad := range map[string]*S3{"a wrong secret": &wrongSecret, "an unknown access key": &unknownKey} {
			calls := map[string]error{
				"check":  bad.Check(ctx),
				"put":    bad.Put(ctx, key+"-x", content, digest(content)),
				"delete": bad.Delete(ctx, key),
			}
			_, calls["get"] = bad.Get(ctx, key, digest(content), len(content))
			_, _, calls["list"] = bad.List(ctx, "client-test/", "", 10)
			for call, err := range calls {
				var o *r.ObjectStoreError
				if !errors.As(err, &o) || o.Status != 403 || !errors.Is(err, r.ErrUnavailable) || !r.IsObjectStoreError(err) {
					t.Errorf("%s, %s: %v", name, call, err)
				}
				noKey(name+", "+call, err)
			}
		}
		// The restarted server, with the key pair the store accepts, reads what the old one wrote: the objects are not signed by the key.
		got, err := c.Get(ctx, key, digest(content), len(content))
		if err != nil || string(got) != string(content) {
			t.Errorf("the restarted client does not read what was written: %q %v", got, err)
		}
		// Nothing was written or deleted by the clients the store refused.
		if _, err := c.Get(ctx, key+"-x", digest(content), len(content)); err == nil {
			t.Error("a refused put wrote an object")
		}
	})
	t.Run("keys_with_awkward_characters_round_trip", func(t *testing.T) {
		prefix := fmt.Sprintf("client-test/awkward-%d/", time.Now().UnixNano())
		for _, name := range []string{"a b", "a+b", "a=b&c", "ünï", "tilde~dollar$", "star*paren(", "中文"} {
			b := []byte("content of " + name)
			if err := c.Put(ctx, prefix+name, b, digest(b)); err != nil {
				t.Fatalf("%q: %v", name, err)
			}
			got, err := c.Get(ctx, prefix+name, digest(b), len(b))
			if err != nil || string(got) != string(b) {
				t.Fatalf("%q: %q %v", name, got, err)
			}
			if err := c.Delete(ctx, prefix+name); err != nil {
				t.Fatalf("%q: %v", name, err)
			}
		}
	})
	t.Run("the_largest_artifact_round_trips", func(t *testing.T) {
		b := make([]byte, r.MaxArtifactBytes)
		for i := range b {
			b[i] = byte(i * 7)
		}
		key := fmt.Sprintf("client-test/large-%d", time.Now().UnixNano())
		if err := c.Put(ctx, key, b, digest(b)); err != nil {
			t.Fatal(err)
		}
		defer c.Delete(ctx, key)
		got, err := c.Get(ctx, key, digest(b), len(b))
		if err != nil || len(got) != len(b) {
			t.Fatal(len(got), err)
		}
	})
}
