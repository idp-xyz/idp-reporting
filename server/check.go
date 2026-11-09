package server

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"idp.local/reporting/identity/signin"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
)

// Report is what check-config found: counts and names of configuration, never a value read from a file or the environment.
type Report struct {
	Connections    int
	Adapters       int // registered adapters (0: names not checked)
	CursorKeys     int
	TrustedKIDs    []string
	Bindings       int
	TLS            bool
	Listen         string
	ObjectStore    bool
	PDF            bool
	Directories    int
	EmbedIntegr    int
	Workers        int
	Quotas         [4]int
	StoreDSNSet    bool
	ObjectKeysSet  bool
	CertExpiresIn  time.Duration
	SweepSeconds   int
	AllowSimulator bool
	// ADR 0016: the digests the start line and every reload log, so that a reviewed file can be matched to a running server.
	BindingsDigest, ConnectionsDigest string
	DesignStore                       string
	// S15-A: the console's browser sign-in, and whether a confidential client's secret is in the environment.
	SignIn, SignInSecretSet bool
}

// Print writes the report as lines of `name: value`.
func (p Report) Print(w io.Writer) {
	fmt.Fprintln(w, "configuration: ok")
	fmt.Fprintf(w, "listen: %s (tls %t)\n", p.Listen, p.TLS)
	fmt.Fprintf(w, "connections: %d (sha256 %s)\n", p.Connections, p.ConnectionsDigest)
	if p.Adapters > 0 {
		fmt.Fprintf(w, "adapters: %d registered, every connection's adapter name found\n", p.Adapters)
	} else {
		fmt.Fprintln(w, "adapters: none registered in this binary; the names are not checked")
	}
	fmt.Fprintf(w, "cursor keys: %d\n", p.CursorKeys)
	fmt.Fprintf(w, "trusted kids: %d\n", len(p.TrustedKIDs))
	fmt.Fprintf(w, "bindings: %d (sha256 %s), every product served by a connection of its tenant\n", p.Bindings, p.BindingsDigest)
	if p.DesignStore == "database" {
		fmt.Fprintln(w, "designs: in the store (shared by every server on it)")
	} else {
		fmt.Fprintln(w, "designs: in the private directories (one server each)")
	}
	fmt.Fprintf(w, "workers: %d\n", p.Workers)
	fmt.Fprintf(w, "quotas: jobs_in_flight %d, stored_results %d, admissions_per_hour %d, queries_in_flight %d\n", p.Quotas[0], p.Quotas[1], p.Quotas[2], p.Quotas[3])
	fmt.Fprintf(w, "object store: %t, pdf: %t, private directories: %d, embedding integrations: %d, sweep seconds: %d, simulators allowed: %t\n", p.ObjectStore, p.PDF, p.Directories, p.EmbedIntegr, p.SweepSeconds, p.AllowSimulator)
	fmt.Fprintf(w, "browser sign-in: %t\n", p.SignIn)
	if p.TLS {
		fmt.Fprintf(w, "certificate: valid for another %d days\n", int(p.CertExpiresIn.Hours()/24))
	}
	set := func(b bool) string {
		if b {
			return "set"
		}
		return "not set"
	}
	fmt.Fprintf(w, "environment: %s %s", StoreDSNEnv, set(p.StoreDSNSet))
	if p.ObjectStore {
		fmt.Fprintf(w, ", object store keys %s", set(p.ObjectKeysSet))
	}
	if p.SignIn {
		fmt.Fprintf(w, ", %s %s (a public client needs none)", signin.ClientSecretEnv, set(p.SignInSecretSet))
	}
	fmt.Fprintln(w)
}

// Check validates the configuration and the files it names without opening anything: the files' presence, kind, owner, mode and size, the
// JWKS and the bindings as accessjwt reads them, the cursor keys, the certificate and its key, the quotas, the embed origins, and the adapter
// names against the registry when it has entries. It returns the first problem; its text names the field and the rule and never a value.
func Check(cfg Config, reg Registry, getenv func(string) string) (Report, error) {
	var rep Report
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if err := cfg.Validate(); err != nil {
		return rep, err
	}
	keys, err := readCursorKeys(cfg.CursorKeyFiles)
	if err != nil {
		return rep, err
	}
	rep.CursorKeys = len(keys)
	verifier, files, err := newVerifier(cfg.Issuer, cfg.Connections)
	if err != nil {
		return rep, err
	}
	rep.TrustedKIDs, rep.Bindings = verifier.KeyIDs(), len(files.bindings)
	rep.BindingsDigest, rep.ConnectionsDigest = files.digest, connectionsDigest(cfg.Connections)
	rep.DesignStore = cfg.DesignStore
	if cfg.TLSCertFile != "" {
		tc, err := loadTLS(cfg.TLSCertFile, cfg.TLSKeyFile, time.Now())
		if err != nil {
			return rep, err
		}
		rep.TLS = true
		if leaf, perr := parseLeaf(tc.Certificates[0].Certificate[0]); perr == nil {
			rep.CertExpiresIn = time.Until(leaf)
		}
	}
	if o := cfg.ObjectStore; o != nil {
		rep.ObjectStore = true
		if o.CAFile != "" {
			if _, err := readTrustedFile("object_store.ca_file", o.CAFile, publicFile, maxCertFileBytes); err != nil {
				return rep, err
			}
		}
		rep.ObjectKeysSet = getenv(objects.AccessKeyEnv) != "" && getenv(objects.SecretKeyEnv) != ""
	}
	if si := cfg.SignIn; si != nil {
		rep.SignIn = true
		if si.CAFile != "" {
			if _, err := readTrustedFile("sign_in.ca_file", si.CAFile, publicFile, maxCertFileBytes); err != nil {
				return rep, err
			}
		}
		secret := getenv(signin.ClientSecretEnv)
		rep.SignInSecretSet = secret != ""
		if _, err := signin.New(si.Client(secret)); err != nil {
			return rep, fmt.Errorf("sign_in is refused with %s or its ca_file (the value is not shown)", signin.ClientSecretEnv)
		}
	}
	if p := cfg.PDF; p != nil {
		rep.PDF = true
		if info, err := os.Stat(p.Typst); err != nil || !info.Mode().IsRegular() {
			return rep, &fileProblem{"pdf.typst", "the file does not exist or is not a regular file"}
		}
		if info, err := os.Stat(p.Fonts); err != nil || !info.IsDir() {
			return rep, &fileProblem{"pdf.fonts", "the directory does not exist"}
		}
	}
	for field, dir := range map[string]string{"layout_directory": cfg.LayoutDir, "dashboard_directory": cfg.DashboardDir, "template_directory": cfg.TemplateDir, "background_directory": cfg.BackgroundDir} {
		if dir == "" {
			continue
		}
		rep.Directories++
		if err := checkPrivateDirectory(field, dir); err != nil {
			return rep, err
		}
	}
	if len(reg) > 0 {
		rep.Adapters = len(reg)
		for i, c := range cfg.Connections {
			if reg[c.Adapter] == nil {
				return rep, fmt.Errorf("connections[%d].adapter names an adapter this binary does not register", i)
			}
		}
	}
	embed, err := cfg.Embedding()
	if err != nil {
		return rep, err
	}
	rep.EmbedIntegr = len(embed.Integrations())
	q, _ := cfg.QuotaValues()
	rep.Quotas = [4]int{q.JobsInFlight, q.StoredResults, q.AdmissionsPerHour, q.QueriesInFlight}
	rep.Connections, rep.Workers, rep.Listen = len(cfg.Connections), cfg.WorkerCount(), cfg.Listen
	rep.SweepSeconds, rep.AllowSimulator = cfg.Retention.SweepInterval, cfg.AllowSimulator
	if dsn := getenv(StoreDSNEnv); dsn != "" {
		rep.StoreDSNSet = true
		if _, err := postgres.ParseDSN(dsn); err != nil {
			return rep, fmt.Errorf("%s is set but is not a connection string the driver can read (its text is not shown)", StoreDSNEnv)
		}
	}
	return rep, nil
}

// checkPrivateDirectory is the stores' own rule, looked at without opening them: a directory, mode 0700, owned by the user the server runs as.
func checkPrivateDirectory(field, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return &fileProblem{field, "the directory does not exist"}
	}
	if !info.IsDir() {
		return &fileProblem{field, "it is not a directory (a symbolic link is refused)"}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
		return &fileProblem{field, "the directory must be private: mode 0700, owned by the user the server runs as"}
	}
	return nil
}
