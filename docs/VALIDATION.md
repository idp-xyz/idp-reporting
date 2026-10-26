# S02-A validation

Pinned Go 1.26.5. No dependency or generic reporting core changes.

```sh
python3 scripts/run_s02a_checks.py --go /path/to/go1.26.5/bin/go
python3 scripts/verify_s02a_workbooks.py --negative-controls
```

The runner executes the ordinary and race full host suites, vet, build and mod
verify. It explicitly removes S01 PG requirement/DSNs from its child environment:
this phase qualifies the identity/HTTP seam, not another database run. The two
DB entry tests must be reported as SKIP; packages with no test files are listed
separately. Pass events include parent tests and their subtests, not independent
extra scenarios. Inspect evidence/s02a/host_checks.json plus raw JSONL.

Both full runs exercise actual TLS loopback HTTP with a local RSA signer and two
in-memory synthetic sources: snapshot jobs, worker execution, frozen pagination,
XLSX tasks/download, other-subject/tenant denial, issuer/audience/scope validation,
source revocation and binding removal. Four downloaded workbooks (two per run)
are independently read through Python ZIP/XML using a literal filename-bound
dataset/row/total oracle; producing-run digests must match. A temporary-copy
negative control swaps finance/shipment bytes and rebinds those digests; the
semantic oracle must still reject it. Original outputs are never modified.

No actual Keycloak, real product ledger, enterprise membership authority, remote
delegated identity, production key refresh/rotation or source contract was
qualified. Current genuine-source inspection is recorded separately in
source_discovery.json and REAL_SOURCE_DISCOVERY_S02A.md.

S01-B ordinary/race 36-case PG and graceful recovery results remain historical
evidence in evidence/s01b_baseline/. Its launch/source hashes apply to that
accepted baseline only. It includes the older s01a_baseline evidence. Do not
combine those PG passes with the current host suite or call them a current VM
rerun. Historical commands/limitations remain in VALIDATION_S01B.md.

The only old production Go file touched is transport/httpapi/api.go (Bearer
challenges); identity/accessjwt is new. The four generic core files, all source
adapters, storage implementations and go.mod/go.sum remain byte-for-byte as
accepted S01-B. Full source hashes are captured before the qualification run and
must be unchanged afterward. Source discovery hashes pin each inspected file.

Real-source I14 and real-issuer I15 remain unqualified; current state is tracked
in ACCEPTANCE_S02A.json. HTML structural/link validation and isolated extracted
build are packaging checks, not browser visual or production runtime acceptance.
