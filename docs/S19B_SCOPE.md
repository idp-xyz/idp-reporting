# S19-B · The company directory: the source lists the companies a principal may choose (ADR 0015), on the server and in the source protocol

2026-11-11. ADR 0015 (accepted 2026-11-06) decided that a source product may list, for the principal, the companies the principal may
currently choose, so that the console can offer a picker instead of a text field of ids. This slice implements the server side of it:
the optional adapter capability in the core, the simulators, the platform route, and the protocol's optional route for a product behind
the generic adapter `http` (S19-A). The console picker is S09-D and is not in this slice.

## What

B1 · **The core** (`reporting`). `CompanyDirectory`, `Company` and `CompanyList` exactly as ADR 0015 writes them (JSON tags `id`, `name`,
`companies`, `partial`). `Service.ListCompanies(ctx, principal, integration)` resolves the connection as `Catalog` does, with the same
refusals (a product the principal does not hold, no connection, an invalid identity: `AUTH_SCOPE_DENIED`); an adapter without the capability
answers `CompanyDirectoryAnswer{Supported: false, Companies: []}`; otherwise the adapter's list is held to `ValidateCompanyList` and nothing
of a list that does not verify is passed on (`INTEGRITY_FAILURE`). The bounds: at most `MaxCompanies` (1000) entries, each id and name
1..`MaxCompanyBytes` (128) bytes of UTF-8 text without control characters (C0, DEL, U+FFFE, U+FFFF) and not blank, no id twice. The rule is
`plainText`, the one `ValidScheduleName` already applied to a person's text. Nothing is stored, cached across requests or logged.

B2 · **The simulators** (`adapters/contractsim`, `adapters/contractsim/pgsource`). `ListCompanies` lists the principal's current grant
(`Grant.Companies`), each once in the grant's order, with the fixtures' names: `company_demo` → 演示公司, `company_other` → 其他公司, any
other id under itself; `Partial: false`; under the catalog's authority (an unknown principal or another source tenant `denied`, an outage
`unavailable`). `company_other` keeps a name but stays outside `DefaultGrant`, so `PrepareRead` refuses it as before and the console's
refusal path (the manual's figure 10) remains testable through the typed line. The durable simulator lists its stored grant the same way.

B3 · **The platform route**. `GET /api/reporting/v1/companies?integration_id=…` (a `viewer`'s, not audited: a read) answers exactly
`{"supported": true, "partial": false, "companies": [{"id", "name"}, …]}`, or `{"supported": false, "partial": false, "companies": []}` for an
adapter without the capability (200, not an error). `integration_id` exactly once and nothing else, as `/catalog` (else 400); a product the
principal does not hold 403; the source's outage 503; a list outside the bounds 409 `INTEGRITY_FAILURE` with none of it shown. The answer
carries the API's `Cache-Control: no-store`. `GET /capabilities` lists the route. docs/openapi.json documents it (`CompanyDirectory`,
`Company`), held to the handler by `TestOpenAPIMatchesTheBoundRoutes`; docs/API.md has its row.

B4 · **The protocol** (docs/SOURCE_HTTP_PROTOCOL.md revision 1.1; docs/source-openapi.json 1.1, held to the wire types by
`TestTheOpenAPIDescriptionIsTheProtocol`). One additive optional route, `POST /reporting-source/v1/companies`, `{context}` →
`{companies: [{id, name}], partial}`, both fields required; the same error table as the read routes; `unsupported` from a source without
the capability. A source written to the first revision is served unchanged: the platform asks the route only for a connection that declares
it. `protocol.Revision` is 1.

B5 · **The adapter** (`adapters/httpsource`). The connection option `companies: true` (Options/Config `Companies`), declared as `issuance`
is: without it the adapter is unchanged and is not a `CompanyDirectory`; with it the value `New` returns implements `CompanyDirectory` for
every issuance (`Directory`, `AuthorityDirectory`, `GroupAuthorityDirectory`), so that the method set says what the connection declared, as
for an adapter in process. The answer is decoded strictly and held to `ValidateCompanyList`; a missing list or `partial`, or a list outside
the bounds, is `INTEGRITY_FAILURE`; refusals and statuses map by the protocol's table, as every other route's. docs/DEPLOYMENT.md has the
option; the shipped server's end-to-end test declares it.

B6 · **The reference source and the checker.** `sourcetest` serves the route from the wrapped adapter's `CompanyDirectory` (`unsupported`
without one) and `sourcetest.Remote` declares `companies` when the wrapped adapter is a directory. `conformance` gains two rules, checked
only with `Target.Companies` (`reporting-source-check -companies`), as a connection that does not declare the capability never asks:
D1 the directory answers and verifies (at most 1000 companies, each id and name 1..128 characters of text, no id twice); D2 a read naming
the listed companies (the first 100) is accepted, and one naming a company the directory does not list is refused. 22 rules in all.

B7 · **Docs**: this page; ADR 0015 (implementation line), ADR 0022 (decision 6), ADR 0017 (the row); docs/SOURCE_HTTP_PROTOCOL.md (the
route, the bounds, section 7, section 8), docs/source-openapi.json, docs/openapi.json, docs/API.md, docs/DEPLOYMENT.md (`companies`, the
example, the pre-connection check), docs/REAL_SOURCE_ONBOARDING.md (the row), docs/SECURITY_REVIEW.md (a section), docs/S19A_SCOPE.md (A7).

Not done: the console picker and the embed page's query panel (S09-D, which depends on this slice); the baseline measurement of ADR 0015
decision 6 (S09-D).

## Evidence

- The bounds, fail closed (`TestACompanyListIsVerifiedFailClosed`): 7 lists that verify (empty, nil, 1000 partial, texts of 128 bytes), 16
  that do not (1001, an id twice, empty and blank texts, 129 bytes, 43 CJK runes, NUL, newline, tab, DEL, escape, U+FFFE, invalid UTF-8, a
  bad entry among good).
- The service (`TestListCompaniesAnswersTheDirectoryOfTheProduct`): the three shapes, the catalog's refusals (a product not held, no
  connection, an invalid identity, an unknown subject, an outage), four lists that do not verify refused as integrity failures, a nil list
  answered as `[]`, the directory's own error passed on.
- The simulators (`TestTheSimulatorListsTheGrantsCompaniesWithNames`): both fixtures, the default grant, a wider grant with a repeated id
  and an unnamed company, `company_other` refused by a read, every refusal of the catalog's authority.
- The adapter against the reference source (`adapters/httpsource/companies_test.go`): declared for every issuance and not otherwise; the
  grant followed through the protocol; the request's route, method and credential; an incomplete context never sent; a source without the
  capability `unsupported` when declared; 21 tampered lists and 6 malformed bodies all integrity failures with nothing passed on and no
  leak; the inclusive bounds (1000 entries of 128 bytes); the empty list; the error table (outage, 500, 401, denied, budget, unsupported,
  an issue-only code, a code with another status, a 201); a slow source within the timeout; the service in process and over the protocol
  answering the same, and `supported: false` for a source without the capability behind the protocol.
- The route (`TestTheCompaniesRouteAnswersTheThreeShapes`, `TestTheCompaniesRouteIsAViewers`): the three shapes byte for byte, the query
  rules, 403 for a product not held, 503 for an outage, 409 for 1001 entries and a repeated id with no name shown, 401 unauthenticated, the
  viewer role and no audit action; the security-header sweep covers the route (200 and 400).
- The contract (`TestMemoryContract`, `TestMemoryContractOverHTTP`, `TestPostgresContract/direct|over_http`): the scenario
  `company_directory_lists_the_grant`; the durable simulator (`TestDurablePostgresContract/company_directory_lists_the_stored_grant`,
  PostgreSQL only). The shipped server (`TestTheShippedRegistryServesAnHTTPSourceEndToEnd`) declares `companies: true` and answers the
  directory of the simulator behind the protocol.
- The checker (`cmd/reporting-source-check`): the reference source passes 22 of 22 with `-issue -companies`; without the flags D1, D2 and
  I1 are skipped and a source without the directory passes; a source that declares the capability without implementing it fails D1
  (`unsupported`), a repeated id fails D1, a directory that lists a company its reads refuse fails D2, none of them showing an id or a name.
- `gofmt -l .` empty, `go vet ./...`, `go test ./...` (the PostgreSQL and object-store tests skip without their environment, as in CI).

## Known

- The console still types company ids: S09-D turns the field into a picker from this route.
- The checker's D2 proves consistency between the directory and the reads for one subject at one instant, not that a product filters the
  list by the current rights in every case; that remains the product's obligation (docs/REAL_SOURCE_ONBOARDING.md).
- "1..128 characters" of ADR 0015 is enforced as 128 bytes of UTF-8 (the bound `PrepareRead` already applies to a company id), so a
  Chinese name of more than 42 characters is refused; no fixture or known product comes near it.
- Not tested against a real product.
