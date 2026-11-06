package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// validDoc is a configuration document the loader accepts; the refusals below change one thing in it.
func validDoc() map[string]any {
	return map[string]any{
		"listen": "127.0.0.1:8443", "tls_cert_file": "/etc/reporting/tls.crt", "tls_key_file": "/etc/reporting/tls.key",
		"issuer":           map[string]any{"issuer": "https://issuer.example", "audience": "reporting", "required_scope": "reporting.read", "max_token_lifetime": 900, "jwks_file": "/etc/reporting/jwks.json", "bindings_file": "/etc/reporting/bindings.json"},
		"cursor_key_files": []string{"/etc/reporting/cursor1.key"},
		"connections":      []map[string]any{{"tenant": "t", "integration": "i", "source_tenant": "s", "adapter": "a", "options": map[string]any{"base": "x"}}},
	}
}

func encode(t testing.TB, doc map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConfigAcceptsAValidDocument(t *testing.T) {
	c, err := ParseConfig(encode(t, validDoc()))
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkerCount() != DefaultWorkers || c.Label() != DefaultProfileLabel || c.AllowSimulator {
		t.Fatalf("defaults: %+v", c)
	}
	if q, err := c.QuotaValues(); err != nil || q.JobsInFlight != 8 || q.QueriesInFlight != 4 {
		t.Fatalf("%+v %v", q, err)
	}
}

// Every refusal of the file itself, each with a known secret placed where it would hurt, and the text of the refusal checked for it.
func TestConfigRefusalsCarryNoSecret(t *testing.T) {
	const secret = "TOPSECRET-value-Zq7"
	with := func(f func(map[string]any)) []byte {
		d := validDoc()
		f(d)
		return encode(t, d)
	}
	issuer := func(d map[string]any) map[string]any { return d["issuer"].(map[string]any) }
	conn := func(d map[string]any) map[string]any { return d["connections"].([]map[string]any)[0] }
	cases := map[string][]byte{
		"an unknown field":          with(func(d map[string]any) { d["listn"] = secret }),
		"a field in the wrong case": with(func(d map[string]any) { d["Listen"] = d["listen"]; delete(d, "listen") }),
		"an unknown nested field":   with(func(d map[string]any) { issuer(d)["extra"] = secret }),
		"an inline dsn":             with(func(d map[string]any) { d["dsn"] = "postgresql://u:" + secret + "@h/db" }),
		"an inline store dsn":       with(func(d map[string]any) { d["store"] = map[string]any{"dsn": secret} }),
		"an inline password":        with(func(d map[string]any) { d["password"] = secret }),
		"an inline secret":          with(func(d map[string]any) { issuer(d)["client_secret"] = secret }),
		"an inline token":           with(func(d map[string]any) { d["token"] = secret }),
		"an inline key":             with(func(d map[string]any) { d["cursor_key"] = secret }),
		"an inline access key": with(func(d map[string]any) {
			d["object_store"] = map[string]any{"endpoint": "https://s3.example", "bucket": "b", "access_key": secret}
		}),
		"a secret inside adapter options": with(func(d map[string]any) {
			conn(d)["options"] = map[string]any{"nested": map[string]any{"api_password": secret}}
		}),
		"a secret in a list in options": with(func(d map[string]any) {
			conn(d)["options"] = map[string]any{"list": []any{map[string]any{"Token": secret}}}
		}),
		"a wrong type":                       with(func(d map[string]any) { issuer(d)["max_token_lifetime"] = secret }),
		"a fraction":                         with(func(d map[string]any) { issuer(d)["max_token_lifetime"] = 12.5 }),
		"a null":                             []byte(`{"listen":null}`),
		"a lifetime of zero":                 with(func(d map[string]any) { issuer(d)["max_token_lifetime"] = 0 }),
		"a lifetime over an hour":            with(func(d map[string]any) { issuer(d)["max_token_lifetime"] = 3601 }),
		"no cursor key file":                 with(func(d map[string]any) { d["cursor_key_files"] = []string{} }),
		"a ring of five":                     with(func(d map[string]any) { d["cursor_key_files"] = []string{"/a", "/b", "/c", "/d", "/e"} }),
		"the same key file twice":            with(func(d map[string]any) { d["cursor_key_files"] = []string{"/a", "/a"} }),
		"a relative path":                    with(func(d map[string]any) { d["tls_cert_file"] = "tls.crt" }),
		"no connection":                      with(func(d map[string]any) { d["connections"] = []map[string]any{} }),
		"a repeated connection":              with(func(d map[string]any) { d["connections"] = []map[string]any{conn(d), conn(d)} }),
		"a connection without an adapter":    with(func(d map[string]any) { conn(d)["adapter"] = "" }),
		"options that are not an object":     with(func(d map[string]any) { conn(d)["options"] = []int{1} }),
		"a quota out of its bound":           with(func(d map[string]any) { d["quotas"] = map[string]any{"jobs_in_flight": 1001} }),
		"a negative quota":                   with(func(d map[string]any) { d["quotas"] = map[string]any{"queries_in_flight": -1} }),
		"max_in_flight out of its bound":     with(func(d map[string]any) { d["max_in_flight"] = 4097 }),
		"workers out of their bound":         with(func(d map[string]any) { d["workers"] = 33 }),
		"a pool smaller than workers plus 2": with(func(d map[string]any) { d["workers"] = 4; d["store"] = map[string]any{"pool_max_conns": 5} }),
		"a pool above 100":                   with(func(d map[string]any) { d["store"] = map[string]any{"pool_max_conns": 101} }),
		"a shutdown grace out of its bound":  with(func(d map[string]any) { d["shutdown_grace"] = 601 }),
		"a sweep interval under a minute":    with(func(d map[string]any) { d["retention"] = map[string]any{"sweep_interval": 5} }),
		"a sweep batch over its bound":       with(func(d map[string]any) { d["retention"] = map[string]any{"batch": 10001} }),
		"half a pdf section":                 with(func(d map[string]any) { d["pdf"] = map[string]any{"typst": "/usr/bin/typst"} }),
		"an object store without a bucket":   with(func(d map[string]any) { d["object_store"] = map[string]any{"endpoint": "https://s3.example"} }),
		"an embed origin that is http":       with(func(d map[string]any) { d["embed_origins"] = map[string]any{"i": []string{"http://127.0.0.1:1"}} }),
		"an embed origin for no connection":  with(func(d map[string]any) { d["embed_origins"] = map[string]any{"other": []string{"https://app.example"}} }),
		"a profile label with a newline":     with(func(d map[string]any) { d["profile_label"] = "a\nb" }),
		"a certificate without its key":      with(func(d map[string]any) { delete(d, "tls_key_file") }),
		"plain and TLS together":             with(func(d map[string]any) { d["allow_plain_loopback"] = true }),
		"neither TLS nor plain":              with(func(d map[string]any) { delete(d, "tls_cert_file"); delete(d, "tls_key_file") }),
		"metrics on a public address":        with(func(d map[string]any) { d["metrics_listen"] = "0.0.0.0:9090" }),
		"metrics on a host name":             with(func(d map[string]any) { d["metrics_listen"] = "localhost:9090" }),
		"metrics without a port":             with(func(d map[string]any) { d["metrics_listen"] = "127.0.0.1" }),
		"metrics on the API's address":       with(func(d map[string]any) { d["metrics_listen"] = d["listen"] }),
		"plain HTTP on a public address": with(func(d map[string]any) {
			delete(d, "tls_cert_file")
			delete(d, "tls_key_file")
			d["allow_plain_loopback"] = true
			d["listen"] = "0.0.0.0:8080"
		}),
		"plain HTTP on every interface": with(func(d map[string]any) {
			delete(d, "tls_cert_file")
			delete(d, "tls_key_file")
			d["allow_plain_loopback"] = true
			d["listen"] = ":8080"
		}),
		"plain HTTP on a host name": with(func(d map[string]any) {
			delete(d, "tls_cert_file")
			delete(d, "tls_key_file")
			d["allow_plain_loopback"] = true
			d["listen"] = "localhost:8080"
		}),
		"a listen without a port":        with(func(d map[string]any) { d["listen"] = "127.0.0.1" }),
		"a duplicate key":                []byte(`{"listen":"127.0.0.1:1","listen":"` + secret + `"}`),
		"a duplicate nested key":         []byte(`{"issuer":{"audience":"a","audience":"` + secret + `"}}`),
		"a duplicate key in the options": []byte(`{"connections":[{"options":{"a":1,"a":"` + secret + `"}}]}`),
		"data after the document":        append(encode(t, validDoc()), []byte(` {"x":"`+secret+`"}`)...),
		"not JSON":                       []byte(`listen = "` + secret + `"`),
		"an empty file":                  {},
		"a file that is not UTF-8":       append([]byte(`{"profile_label":"`), append([]byte{0xff, 0xfe}, []byte(secret+`"}`)...)...),
		"a list at the top":              []byte(`["` + secret + `"]`),
		"a document nested too deeply":   []byte(strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40)),
		"a file over 256 KiB":            append(encode(t, validDoc()), []byte(strings.Repeat(" ", MaxConfigBytes))...),
	}
	for name, doc := range cases {
		_, err := ParseConfig(doc)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Errorf("%s: not a ConfigError: %v", name, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "postgresql://") {
			t.Errorf("%s: the refusal carries a value of the file: %q", name, err)
		}
	}
	// The three that name secrets say so, and neither of them says which value.
	_, err := ParseConfig(with(func(d map[string]any) { d["dsn"] = secret }))
	if err == nil || !strings.Contains(err.Error(), "named like a secret") {
		t.Errorf("an inline dsn: %v", err)
	}
	_, err = ParseConfig(with(func(d map[string]any) { d["listn"] = secret }))
	if err == nil || strings.Contains(err.Error(), "named like a secret") || !strings.Contains(err.Error(), "does not know") {
		t.Errorf("an unknown field: %v", err)
	}
}

func TestSecretLikeNames(t *testing.T) {
	for _, n := range []string{"dsn", "DSN", "store_dsn", "password", "db_password", "Secret", "client_secret", "token", "access_token", "api_key", "key", "private_key", "secret_key", "accessKey", "credentials", "passphrase", "bearer", "jwt"} {
		if !secretLike(n) {
			t.Errorf("%q is not read as a secret", n)
		}
	}
	for _, n := range []string{"listen", "cursor_key_files", "tls_key_file", "key_file", "jwks_file", "base_url", "region", "timeout"} {
		if secretLike(n) {
			t.Errorf("%q is read as a secret", n)
		}
	}
}

// The configuration's schema has no field that could reach a test-only option, and no field that could hold a secret: the shipped command's
// loader refuses the names that would, which is what keeps InsecureTestOnly out of production.
func TestShippedCommandCannotReachTheTestOnlyOptions(t *testing.T) {
	var names []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			names = append(names, name)
			walk(typ.Field(i).Type)
		}
	}
	walk(reflect.TypeOf(Config{}))
	for _, n := range names {
		l := strings.ToLower(n)
		if strings.Contains(l, "insecure") || strings.Contains(l, "test") || strings.Contains(l, "skip") || strings.Contains(l, "verify") || strings.Contains(l, "approved") || l == "profile" {
			t.Errorf("the configuration has a field %q", n)
		}
	}
	for _, field := range []string{"insecure_test_only", "insecure", "sslmode", "production_approved", "skip_tls_verify"} {
		doc := validDoc()
		doc[field] = true
		if _, err := ParseConfig(encode(t, doc)); err == nil {
			t.Errorf("the loader accepts %q", field)
		}
	}
	// And the source: the command and the Main it calls name no ForTests option.
	for _, file := range []string{"../cmd/reporting-server/main.go", "main.go", "check.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "ForTests(") || strings.Contains(string(src), "InsecureTestOnly") {
			t.Errorf("%s names a test-only option", file)
		}
	}
	// The only file of the package that mentions the InsecureTestOnly of the store is the one that passes the option the tests set.
	entries, _ := filepath.Glob("*.go")
	for _, file := range entries {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(file)
		if strings.Contains(string(src), "InsecureTestOnly") && file != "server.go" && file != "testing.go" {
			t.Errorf("%s names InsecureTestOnly", file)
		}
	}
}
