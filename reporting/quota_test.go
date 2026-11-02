package reporting_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// The quotas of an identity (S04-D, docs/S04D_SCOPE.md D1): zero is the default, a value outside its bounds is refused at construction.
func TestQuotasTakeDefaultsAndRefuseWhatIsOutOfBounds(t *testing.T) {
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	service := func(q r.Quotas) (*r.Service, error) {
		return r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKey: []byte("01234567890123456789012345678901"), Quotas: q})
	}
	s, e := service(r.Quotas{})
	if e != nil || s.Quotas() != (r.Quotas{JobsInFlight: 8, StoredResults: 64, AdmissionsPerHour: 120}) {
		t.Fatalf("%+v %v", s, e)
	}
	// A value inside its bounds is kept, at both ends of each range; the others default.
	for _, q := range []r.Quotas{{JobsInFlight: 1}, {JobsInFlight: 1000}, {StoredResults: 1}, {StoredResults: 100000}, {AdmissionsPerHour: 1}, {AdmissionsPerHour: 100000}, {JobsInFlight: 3, StoredResults: 5, AdmissionsPerHour: 7}} {
		s, e := service(q)
		if e != nil {
			t.Fatalf("%+v: %v", q, e)
		}
		got := s.Quotas()
		for _, f := range [][2]int{{q.JobsInFlight, got.JobsInFlight}, {q.StoredResults, got.StoredResults}, {q.AdmissionsPerHour, got.AdmissionsPerHour}} {
			if f[0] != 0 && f[0] != f[1] {
				t.Fatalf("%+v became %+v", q, got)
			}
		}
	}
	for _, q := range []r.Quotas{{JobsInFlight: -1}, {JobsInFlight: 1001}, {StoredResults: -1}, {StoredResults: 100001}, {AdmissionsPerHour: -1}, {AdmissionsPerHour: 100001}, {JobsInFlight: 1 << 40}} {
		if s, e := service(q); !errors.Is(e, r.ErrInvalid) || s != nil {
			t.Fatalf("%+v was accepted: %v", q, e)
		}
	}
}

// A refusal names the quota and nothing else: no count, no value, nobody else's.
func TestQuotaErrorNamesOnlyTheQuota(t *testing.T) {
	for _, name := range []string{r.QuotaJobsInFlight, r.QuotaStoredResults, r.QuotaAdmissionsPerHour} {
		e := fmt.Errorf("admitting: %w", &r.QuotaError{Quota: name})
		if !errors.Is(e, r.ErrQuota) || r.QuotaOf(e) != name || errors.Unwrap(e).Error() != "quota exceeded: "+name {
			t.Fatalf("%s: %v", name, e)
		}
		if errors.Is(e, r.ErrInvalid) || errors.Is(e, r.ErrBudget) {
			t.Fatal("a quota refusal is another refusal too")
		}
	}
	if r.QuotaOf(r.ErrQuota) != "" || r.QuotaOf(nil) != "" || r.QuotaOf(r.ErrInvalid) != "" {
		t.Fatal("a quota is named where none was reached")
	}
}
