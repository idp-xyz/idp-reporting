package capacity

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"io"
)

// Growth runs the real service (snapshot, export) for result sets of several sizes on the fixture database, with and without the object store,
// and prints what each stored row weighs (G4): the job at admission and as retention reduces it, the snapshot row, the artifact row with its
// file inline or in the bucket. The rows it makes are removed.
func Growth(ctx context.Context, cfg Config) error {
	cfg.DSN = noParallel(cfg.DSN)
	admin, err := fixturedb.OpenSized(ctx, cfg.DSN, 8, "reporting_test_s01")
	if err != nil {
		return err
	}
	defer admin.Close()
	if port, err := dsnPort(cfg.DSN); err != nil || port == 55432 {
		return fmt.Errorf("refusing this DSN")
	}
	st, err := postgres.Open(ctx, cfg.DSN, true)
	if err != nil {
		return err
	}
	st.Close()
	var bucket r.ObjectStore
	if cfg.ObjectEndpoint != "" {
		s3, err := objects.NewS3(objects.S3{Endpoint: cfg.ObjectEndpoint, Bucket: "reporting-test-s04e", Region: "us-east-1", AccessKey: cfg.ObjectAccess, SecretKey: cfg.ObjectSecret, LoopbackHTTP: true})
		if err != nil {
			return err
		}
		bucket = s3
	}
	for _, rowsN := range []int{3, 100, 1000} {
		for _, withObjects := range []bool{false, true} {
			if withObjects && bucket == nil {
				continue
			}
			var store r.ObjectStore
			if withObjects {
				store = bucket
			}
			if err := growthCase(ctx, cfg.DSN, admin, rowsN, store, cfg.Out); err != nil {
				return err
			}
		}
	}
	return nil
}

func growthCase(ctx context.Context, dsn string, admin *pgxpool.Pool, rowsN int, objs r.ObjectStore, w io.Writer) error {
	q := contractsim.FinancialQuery()
	q.Page.Limit = 100
	var records []contractsim.Record
	var keys []string
	for i := 0; i < rowsN; i++ {
		key := fmt.Sprintf("r%05d", i)
		keys = append(keys, key)
		records = append(records, contractsim.Record{Company: "company_demo", Key: key, Values: []string{fmt.Sprintf("customer_%05d", i), "current", fmt.Sprintf("%d.25", 100+i)}})
	}
	ds := r.Dataset{Ref: q.Dataset, Dimensions: q.Dimensions, Metrics: q.Metrics, CurrencyPolicy: q.CurrencyPolicy, Timezone: q.Timezone, Columns: []r.Column{{ID: "customer.id", Label: "客户", Type: "string"}, {ID: "aging.bucket", Label: "账龄区间", Type: "string"}, {ID: "ar.open_balance", Label: "应收余额", Type: "decimal", Scale: 2, Currency: "CNY"}}}
	src := contractsim.New(ds, records)
	p := contractsim.DemoPrincipal()
	p.Subject = "growth-" + fmt.Sprint(rowsN)
	grant := contractsim.DefaultGrant()
	grant.Rows = keys
	src.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), grant)
	store, err := postgres.Open(ctx, dsn, false, postgres.WithObjects(objs))
	if err != nil {
		return err
	}
	defer store.Close()
	s, err := r.NewService(r.Config{Repository: store, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: src}}, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}, Objects: objs})
	if err != nil {
		return err
	}
	var jobIDs []string
	defer func() {
		_, _ = admin.Exec(context.Background(), "DELETE FROM reporting_s01.assets WHERE subject=$1", p.Subject)
		_, _ = admin.Exec(context.Background(), "DELETE FROM reporting_s01.jobs WHERE subject=$1", p.Subject)
	}()
	snapJob, err := s.SubmitSnapshot(ctx, p, q, "g1")
	if err != nil {
		return err
	}
	jobIDs = append(jobIDs, snapJob.ID)
	var admitted int
	if err = admin.QueryRow(ctx, "SELECT pg_column_size(j.*)::int FROM reporting_s01.jobs j WHERE id=$1", snapJob.ID).Scan(&admitted); err != nil {
		return err
	}
	if _, err = s.RunOne(ctx); err != nil {
		return err
	}
	snapJob, _ = s.Job(ctx, p, snapJob.ID)
	exportJob, err := s.SubmitExport(ctx, p, snapJob.SnapshotID, "g2")
	if err != nil {
		return err
	}
	jobIDs = append(jobIDs, exportJob.ID)
	if _, err = s.RunOne(ctx); err != nil {
		return err
	}
	exportJob, _ = s.Job(ctx, p, exportJob.ID)
	var done, reduced, snapshot, artifact, artifactCol int
	var content int
	if err = admin.QueryRow(ctx, `SELECT pg_column_size(j.*)::int, pg_column_size((j.data - 'Query' - 'Prepared' - 'Columns') || '{"Reduced": true}'::jsonb)::int + (pg_column_size(j.*) - pg_column_size(j.data))::int
FROM reporting_s01.jobs j WHERE id=$1`, snapJob.ID).Scan(&done, &reduced); err != nil {
		return err
	}
	if err = admin.QueryRow(ctx, "SELECT pg_column_size(a.*)::int, octet_length(a.data::text) FROM reporting_s01.assets a WHERE id=$1", snapJob.SnapshotID).Scan(&snapshot, new(int)); err != nil {
		return err
	}
	if err = admin.QueryRow(ctx, "SELECT pg_column_size(a.*)::int, pg_column_size(a.data)::int FROM reporting_s01.assets a WHERE id=$1", exportJob.ArtifactID).Scan(&artifact, &artifactCol); err != nil {
		return err
	}
	art, err := s.Download(ctx, p, exportJob.ArtifactID)
	if err != nil {
		return err
	}
	content = len(art.Content)
	where := "inline in the row"
	if objs != nil {
		where = "in the object store"
	}
	fmt.Fprintf(w, "growth   result rows=%-5d file %-20s job at admission=%d B; job finished=%d B; job as reduced=%d B; snapshot row=%d B; artifact row=%d B (data column %d B); the .xlsx file itself=%d B\n", rowsN, where, admitted, done, reduced, snapshot, artifact, artifactCol, content)
	return nil
}
