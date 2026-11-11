package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSecretFilesMustBePrivateRegularAndNotSymlinks(t *testing.T) {
	const keyText = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir := t.TempDir()
	good := filepath.Join(dir, "good.key")
	writeFile(t, good, []byte(keyText), 0o600)
	if _, err := readCursorKeys([]string{good}); err != nil {
		t.Fatalf("0600: %v", err)
	}
	ro := filepath.Join(dir, "ro.key")
	writeFile(t, ro, []byte(base64.StdEncoding.EncodeToString([]byte(keyText))), 0o400)
	if k, err := readCursorKeys([]string{ro}); err != nil || len(k[0]) != 64 {
		t.Fatalf("0400 and base64: %v", err)
	}
	cases := map[string]func() string{
		"mode 0644": func() string { p := filepath.Join(dir, "m644"); writeFile(t, p, []byte(keyText), 0o644); return p },
		"mode 0640": func() string { p := filepath.Join(dir, "m640"); writeFile(t, p, []byte(keyText), 0o640); return p },
		"mode 0660": func() string { p := filepath.Join(dir, "m660"); writeFile(t, p, []byte(keyText), 0o660); return p },
		"mode 0700": func() string { p := filepath.Join(dir, "m700"); writeFile(t, p, []byte(keyText), 0o700); return p },
		"mode 0200": func() string { p := filepath.Join(dir, "m200"); writeFile(t, p, []byte(keyText), 0o200); return p },
		"a symlink to a good file": func() string {
			p := filepath.Join(dir, "link")
			if err := os.Symlink(good, p); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a symlink within its directory to a good file": func() string {
			p := filepath.Join(dir, "samedir")
			if err := os.Symlink("good.key", p); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a directory": func() string { return dir },
		"a fifo": func() string {
			p := filepath.Join(dir, "fifo")
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		},
		"a missing file": func() string { return filepath.Join(dir, "missing") },
		"too short": func() string {
			p := filepath.Join(dir, "short")
			writeFile(t, p, []byte("00112233445566778899aabbccddeeff"), 0o600)
			return p
		},
		"too long": func() string {
			p := filepath.Join(dir, "long")
			writeFile(t, p, []byte(strings.Repeat("ab", 65)), 0o600)
			return p
		},
		"not an encoding": func() string {
			p := filepath.Join(dir, "garbage")
			writeFile(t, p, []byte("this is not a key !!! "+keyText), 0o600)
			return p
		},
		"empty": func() string { p := filepath.Join(dir, "empty"); writeFile(t, p, nil, 0o600); return p },
		"oversized": func() string {
			p := filepath.Join(dir, "big")
			writeFile(t, p, []byte(strings.Repeat("a", 5000)), 0o600)
			return p
		},
	}
	for name, make := range cases {
		path := make()
		_, err := readCursorKeys([]string{path})
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		var fp *fileProblem
		if !asFileProblem(err, &fp) {
			t.Errorf("%s: %v", name, err)
		}
		// Neither the content nor the path is in the text; the field is.
		if strings.Contains(err.Error(), keyText[:16]) || strings.Contains(err.Error(), dir) || !strings.HasPrefix(err.Error(), "cursor_key_files[0]: ") {
			t.Errorf("%s: %q", name, err)
		}
	}
	// Two files with the same key are one key twice.
	dup := filepath.Join(dir, "dup.key")
	writeFile(t, dup, []byte(keyText+"\n"), 0o600)
	if _, err := readCursorKeys([]string{good, dup}); err == nil || !strings.Contains(err.Error(), "same key") {
		t.Errorf("a repeated key: %v", err)
	}
	// A symlinked directory above the file is the operator's layout (/var/run, a mounted secret): only the file itself is checked.
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(realDir, "k"), []byte(keyText), 0o600)
	if err := os.Symlink(realDir, filepath.Join(dir, "viadir")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCursorKeys([]string{filepath.Join(dir, "viadir", "k")}); err != nil {
		t.Errorf("a directory symlink above the file: %v", err)
	}
}

func asFileProblem(err error, target **fileProblem) bool {
	fp, ok := err.(*fileProblem)
	if ok {
		*target = fp
	}
	return ok
}

func TestPublicFilesMustNotBeWritableByOthers(t *testing.T) {
	w := newWorld(t)
	if _, _, err := newVerifier(w.cfg.Issuer, w.cfg.Connections); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o666, 0o664, 0o646, 0o622} {
		for _, f := range []string{w.jwks, w.binds} {
			orig, _ := os.ReadFile(f)
			writeFile(t, f, orig, mode)
			_, _, err := newVerifier(w.cfg.Issuer, w.cfg.Connections)
			if err == nil || !strings.Contains(err.Error(), "writable") {
				t.Errorf("%o %s: %v", mode, filepath.Base(f), err)
			}
			writeFile(t, f, orig, 0o644)
		}
	}
	// A symlink is refused for them too.
	link := filepath.Join(w.dir, "jwks-link.json")
	if err := os.Symlink(w.jwks, link); err != nil {
		t.Fatal(err)
	}
	c := w.cfg.Issuer
	c.JWKSFile = link
	if _, _, err := newVerifier(c, w.cfg.Connections); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked jwks: %v", err)
	}
	// And the TLS key is a secret file; the certificate a public one.
	keyMode, _ := os.Stat(w.cfg.TLSKeyFile)
	_ = keyMode
	writeFile(t, w.cfg.TLSKeyFile, mustRead(t, w.cfg.TLSKeyFile), 0o644)
	if _, err := loadTLS(w.cfg.TLSCertFile, w.cfg.TLSKeyFile, time.Now()); err == nil || !strings.Contains(err.Error(), "0600 or 0400") {
		t.Errorf("a readable TLS key: %v", err)
	}
}

func TestBindingsFileIsAStrictList(t *testing.T) {
	for name, doc := range map[string]string{
		"an object":                    `{"subject":"a"}`,
		"an unknown field":             `[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"role":"admin"}]`,
		"a wrong-case field":           `[{"Subject":"a","client_id":"c","tenant":"t","integrations":["i"]}]`,
		"a duplicate key":              `[{"subject":"a","subject":"b","client_id":"c","tenant":"t","integrations":["i"]}]`,
		"trailing data":                `[] []`,
		"a wrong type":                 `[{"subject":1,"client_id":"c","tenant":"t","integrations":["i"]}]`,
		"a null":                       `[null]`,
		"not JSON":                     `subject: a`,
		"an email with a display name": `[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"email":"A <a@example.com>"}]`,
		"an email of two addresses":    `[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"email":"a@example.com, b@example.com"}]`,
		"an email that is not one":     `[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"email":"zhang.san"}]`,
		"an email of a list":           `[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"email":["a@example.com"]}]`,
	} {
		err := func() error { _, err := parseBindings([]byte(doc)); return err }()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "example.com") || strings.Contains(err.Error(), "zhang") {
			t.Errorf("%s: the refusal carries the address: %v", name, err)
		}
	}
	if list, err := parseBindings([]byte(`[]`)); err != nil || len(list) != 0 {
		t.Errorf("an empty list denies everyone and is valid: %v", err)
	}
	list, err := parseBindings([]byte(`[{"subject":"a","client_id":"c","tenant":"t","integrations":["i","j"]}]`))
	if err != nil || len(list) != 1 || list[0].Subject != "a" || len(list[0].Integrations) != 2 || list[0].Email != "" {
		t.Errorf("%+v %v", list, err)
	}
	// S16-B: an email is one address, kept as written; absent is none.
	list, err = parseBindings([]byte(`[{"subject":"a","client_id":"c","tenant":"t","integrations":["i"],"email":"zhang.san@example.com"},{"subject":"b","client_id":"c","tenant":"t","integrations":["i"]}]`))
	if err != nil || len(list) != 2 || list[0].Email != "zhang.san@example.com" || list[1].Email != "" {
		t.Errorf("%+v %v", list, err)
	}
}

func TestIssuerFilesRefusedByTheProfileAreNamedByAFixedText(t *testing.T) {
	w := newWorld(t)
	secret := "N-MODULUS-SECRET-LOOKING-Zq7"
	writeFile(t, w.jwks, []byte(`{"keys":[{"kty":"RSA","kid":"`+secret+`","n":"`+secret+`","e":"AQAB"}]}`), 0o644)
	_, _, err := newVerifier(w.cfg.Issuer, w.cfg.Connections)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("%v", err)
	}
}

func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
