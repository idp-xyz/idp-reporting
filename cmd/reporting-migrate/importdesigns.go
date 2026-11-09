package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"idp.local/reporting/dashboards"
	"idp.local/reporting/doctemplates"
	"idp.local/reporting/layouts"
	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
)

// importOptions is import-designs' input (S11-C, docs/S11C_SCOPE.md): the issuer of the bindings, the bindings file (the identities whose
// documents are looked for: a file in a private directory names only a digest of its owner), the directories by store, and whether a
// document already in the database is replaced.
type importOptions struct {
	issuer   string
	bindings string
	dirs     map[string]string // "layouts", "dashboards", "templates", "backgrounds" -> directory; "images" is backgrounds/images
	replace  bool
}

type importItem struct {
	store string
	owner r.Identity
	data  []byte
}

// importDesigns reads every document of every bound identity from the directories, holds each to its store's rules, and only when all of them
// pass writes them to the database, one transaction per document. It prints counts, never an identity.
func importDesigns(ctx context.Context, pool *pgxpool.Pool, o importOptions, out io.Writer) error {
	if !strings.HasPrefix(o.issuer, "https://") {
		return errors.New("import-designs needs --issuer, the https issuer of the bindings (issuer.issuer of the server's configuration)")
	}
	if len(o.dirs) == 0 {
		return errors.New("import-designs needs at least one of --layout-directory, --dashboard-directory, --template-directory, --background-directory")
	}
	owners, err := bindingIdentities(o.bindings, o.issuer)
	if err != nil {
		return err
	}
	if bg, ok := o.dirs["backgrounds"]; ok {
		if info, err := os.Stat(filepath.Join(bg, "images")); err == nil && info.IsDir() {
			o.dirs["images"] = filepath.Join(bg, "images")
		}
	}
	stores := make([]string, 0, len(o.dirs))
	for s := range o.dirs {
		stores = append(stores, s)
	}
	sort.Strings(stores)
	var items []importItem
	unmatched := map[string]int{}
	for _, store := range stores {
		dir := o.dirs[store]
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("the %s directory does not exist", store)
		}
		files, err := documentFiles(dir)
		if err != nil {
			return fmt.Errorf("the %s directory cannot be listed", store)
		}
		backend, err := privatedocs.OpenDirectory(dir)
		if err != nil {
			return fmt.Errorf("the %s directory cannot be opened (a server still holds it, or it is not private: mode 0700, owned by this user)", store)
		}
		for _, owner := range owners {
			data, err := backend.Load(owner)
			if err != nil {
				backend.Close()
				return fmt.Errorf("a document of the %s directory cannot be read", store)
			}
			if data == nil {
				continue
			}
			delete(files, privatedocs.DirectoryFileName(owner))
			if err := checkDesign(store, owner, data); err != nil {
				backend.Close()
				return fmt.Errorf("a document of the %s directory is refused by the store's rules (%v); nothing was imported", store, err)
			}
			items = append(items, importItem{store: store, owner: owner, data: data})
		}
		backend.Close()
		unmatched[store] = len(files)
	}
	written, kept := map[string]int{}, map[string]int{}
	for _, it := range items {
		ok, err := postgres.ImportDesign(ctx, pool, it.store, it.owner, it.data, o.replace)
		if err != nil {
			return fmt.Errorf("the import stopped at a %s document (%d of %d written): %w", it.store, sum(written), len(items), err)
		}
		if ok {
			written[it.store]++
		} else {
			kept[it.store]++
		}
	}
	for _, store := range stores {
		fmt.Fprintf(out, "%s: %d imported, %d kept (already in the store; --replace overwrites), %d files of no bound identity\n", store, written[store], kept[store], unmatched[store])
	}
	return nil
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// documentFiles lists the owners' files of a private directory (the lock and the temporary files are not documents).
func documentFiles(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			out[e.Name()] = true
		}
	}
	return out, nil
}

// bindingIdentities reads the bindings file strictly (the server's schema: subject, client_id, tenant, integrations) and answers the
// identities it binds, each once.
func bindingIdentities(path, issuer string) ([]r.Identity, error) {
	if path == "" {
		return nil, errors.New("import-designs needs --bindings, the server's issuer.bindings_file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("the bindings file cannot be read")
	}
	var list []struct {
		Subject      string   `json:"subject"`
		ClientID     string   `json:"client_id"`
		Tenant       string   `json:"tenant"`
		Integrations []string `json:"integrations"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&list) != nil {
		return nil, errors.New("the bindings file is not the list of bindings")
	}
	seen := map[r.Identity]bool{}
	var out []r.Identity
	for _, b := range list {
		id := r.Identity{Tenant: b.Tenant, Issuer: issuer, Subject: b.Subject}
		if !id.Valid() {
			return nil, errors.New("the bindings file holds a binding without a tenant or a subject")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// checkDesign holds a document to its store's rules: the generic ones for every store, and the typed store's own validation of every
// version for the layouts, the dashboards and the templates (it is read through a memory backend, as the server would read it).
func checkDesign(store string, owner r.Identity, data []byte) error {
	cap := privatedocs.DefaultCap
	if store == "templates" {
		cap = doctemplates.MaxTemplates
	}
	if err := privatedocs.CheckDocument(data, cap); err != nil {
		return err
	}
	mem := privatedocs.NewMemoryBackend()
	tx, _ := mem.Begin(owner)
	if err := tx.Store(data); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	switch store {
	case "layouts":
		s, err := layouts.OpenBackend(mem)
		if err != nil {
			return err
		}
		_, err = s.List(owner)
		return err
	case "dashboards":
		s, err := dashboards.OpenBackend(mem)
		if err != nil {
			return err
		}
		_, err = s.List(owner)
		return err
	case "templates":
		s, err := doctemplates.OpenBackend(mem)
		if err != nil {
			return err
		}
		_, err = s.List(owner)
		return err
	}
	return nil
}
