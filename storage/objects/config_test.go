package objects

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	r "idp.local/reporting/reporting"
)

func parse(t *testing.T, args ...string) *Flags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	f := Register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return f
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTheKeysAreTheEnvironmentsAndAFlagForEitherIsRefusedWithoutRepeatingIt(t *testing.T) {
	for _, args := range [][]string{
		{"-object-store-secret-key=SECRETVALUE"},
		{"--object-store-secret-key", "SECRETVALUE"},
		{"-object-store-access-key=ACCESSVALUE"},
		{"--object-store-access-key=ACCESSVALUE", "-x"},
		{"-object-store-endpoint=https://s3.example.com", "-object-store-secret-key=SECRETVALUE"},
		{"-object-store-secret=SECRETVALUE"},
		{"-object-store-key=SECRETVALUE"},
		{"-object-store-secret-access-key=SECRETVALUE"},
	} {
		err := RefuseCredentialFlags(args)
		if !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%v: %v", args, err)
			continue
		}
		for _, leak := range []string{"SECRETVALUE", "ACCESSVALUE"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%v: the refusal repeats the value: %q", args, err)
			}
		}
		if !strings.Contains(err.Error(), AccessKeyEnv) || !strings.Contains(err.Error(), SecretKeyEnv) {
			t.Errorf("%v: the refusal does not say where the keys are: %q", args, err)
		}
	}
	// The other flags, and what follows "--", are not credentials.
	for _, args := range [][]string{nil, {"-object-store-endpoint=https://s3.example.com", "-object-store-bucket", "reports"}, {"--", "-object-store-secret-key=x"}, {"-sweep-interval", "0"}} {
		if err := RefuseCredentialFlags(args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	// There is no such flag to set either: the set does not define one.
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	Register(fs)
	if fs.Lookup("object-store-secret-key") != nil || fs.Lookup("object-store-access-key") != nil {
		t.Error("a flag for a key exists")
	}
}

func TestTheClientIsMadeFromTheFlagsAndTheEnvironmentOrNotAtAll(t *testing.T) {
	keys := map[string]string{AccessKeyEnv: "AK0123456789", SecretKeyEnv: "SK0123456789abcdef"}
	if c, err := parse(t).Client(env(keys)); c != nil || err != nil {
		t.Fatalf("no flag: %v %v", c, err)
	}
	c, err := parse(t, "-object-store-endpoint=http://127.0.0.1:9000", "-object-store-bucket=reports-bucket", "-object-store-loopback-http").Client(env(keys))
	if err != nil || c == nil || c.Region != "us-east-1" || c.Bucket != "reports-bucket" {
		t.Fatalf("%v %v", c, err)
	}
	c, err = parse(t, "-object-store-endpoint=https://s3.example.com", "-object-store-bucket=reports-bucket", "-object-store-region=eu-west-1").Client(env(keys))
	if err != nil || c.Region != "eu-west-1" {
		t.Fatalf("%v %v", c, err)
	}
	for name, c := range map[string]struct {
		f *Flags
		e map[string]string
	}{
		"an endpoint with no bucket":     {parse(t, "-object-store-endpoint=https://s3.example.com"), keys},
		"a bucket with no endpoint":      {parse(t, "-object-store-bucket=reports-bucket"), keys},
		"a flag alone":                   {parse(t, "-object-store-loopback-http"), keys},
		"no access key":                  {parse(t, "-object-store-endpoint=https://s3.example.com", "-object-store-bucket=reports-bucket"), map[string]string{SecretKeyEnv: "SK0123456789abcdef"}},
		"no secret key":                  {parse(t, "-object-store-endpoint=https://s3.example.com", "-object-store-bucket=reports-bucket"), map[string]string{AccessKeyEnv: "AK0123456789"}},
		"http off the loopback":          {parse(t, "-object-store-endpoint=http://s3.example.com", "-object-store-bucket=reports-bucket", "-object-store-loopback-http"), keys},
		"http without the explicit flag": {parse(t, "-object-store-endpoint=http://127.0.0.1:9000", "-object-store-bucket=reports-bucket"), keys},
	} {
		got, err := c.f.Client(env(c.e))
		if got != nil || !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v %v", name, got, err)
			continue
		}
		for _, v := range c.e {
			if strings.Contains(err.Error(), v) {
				t.Errorf("%s: the refusal repeats a key: %q", name, err)
			}
		}
	}
}
