# ADR 0015 · The source product lists the companies a principal may choose

Date: 2026-11-06. Status: accepted, by the decision the owner delegated to engineering the same day (ADR 0014 decision 13), and
ratified by the product owner on 2026-11-11, when it was put to the owner again as a contract-level decision (CODING_STANDARDS.md).
It adds an optional capability to the adapter contract; no existing adapter, request or stored record changes.
Implementation: S19-B (server, 2026-11-11: `reporting.CompanyDirectory`, the simulators, `GET /companies`, and the protocol's optional
route `POST /reporting-source/v1/companies` for a connection with `companies: true`; docs/S19B_SCOPE.md); S09-D (the console and the
embed page, 2026-11-11: the picker of decision 4, `console/assets/companies.js`, fail closed to the text field; the measurement of
decision 6; docs/S09D_SCOPE.md, which lists the readings it made of this ADR — among them that one listed company is chosen already).

## Context

The console asks for the company scope of a query as text: ids separated by commas (`#query-companies`, "输入公司 ID，多个以
逗号分隔"). A person does not know ids. A wrong one is refused only when the query runs (`PrepareRead` denies it), and the
refusal names nothing the person can act on (docs/manual/img/10-refused-company.png). The baseline task T1 (sign in, freeze,
export) types two texts: the token and a company id. After ADR 0014's slices S09-A to S09-C this is the largest usability
defect left in the console, and it is the one the console cannot fix alone.

The contract says why. "用户可选择 integration_id 与 company_ids，但它们只是选择器，必须在服务端映射到当前主体获准范围；不能作为
授权依据" (docs/s00/INTERFACES.md, 信任上下文); `DescribeCatalog` returns datasets, not companies; `PrepareRead` and
`AuthorizeCurrent` decide every read against the principal's current rights, which only the source product knows. The
platform never holds a list of what a principal may see, so the console has nothing to offer in a picker, and inventing a
list from past queries would show a company the source has not confirmed for this identity. The onboarding guide's 当前授权
row (docs/REAL_SOURCE_ONBOARDING.md) already asks a product how the current rights cover companies; it does not ask the
product to say which ones.

ADR 0003 added a capability to the contract the same way this one does: an optional interface the adapter may implement
(`reporting.DocumentAuthority`), declared by the adapter, used by the platform when present, and absent without harm.

## Decision

1. **An optional adapter capability.** `reporting.CompanyDirectory` is implemented by an adapter whose source product can
   say which companies the principal may currently choose:

   ```go
   // CompanyDirectory is the optional capability of an adapter whose source product can list the companies
   // the principal may currently choose (ADR 0015). The list is advisory: PrepareRead and AuthorizeCurrent
   // authorize every read as they do without it.
   type CompanyDirectory interface {
       // ListCompanies returns the companies the principal of the context may choose now, in the product's
       // order, each with the id the query names and the name a person reads. Partial is true when the
       // product cut the list (the platform accepts at most 1000 entries); then the person may still type
       // an id. An empty list is an answer: nothing may be chosen.
       ListCompanies(context.Context, SourceContext) (CompanyList, error)
   }
   type Company struct {
       ID   string `json:"id"`   // 1–128 characters, the value PrepareRead receives in Companies
       Name string `json:"name"` // 1–128 characters, what the console shows; never stored by the platform
   }
   type CompanyList struct {
       Companies []Company `json:"companies"`
       Partial   bool      `json:"partial"`
   }
   ```

2. **Advisory, not an authorization.** The list changes nothing in how a read is authorized: `PrepareRead` still refuses a
   company the principal may not query, `AuthorizeCurrent` still confirms the frozen scope at every disclosure, and a
   company that was in the list may be refused a minute later when the rights changed. The platform does not store the
   list, does not cache it across requests, and never logs a name; a frozen snapshot keeps company ids as it does today
   (`PreparedRead.Companies`), never names. The console keeps nothing of it beyond the session (no storage, as for every
   other datum; docs/CONSOLE.md).
3. **The platform route.** `GET /api/reporting/v1/companies?integration_id=…` answers, under the same authority as
   `GET /catalog` (only a product explicitly allowed for the authenticated principal; the same refusals), with exactly
   `{"supported": true, "partial": false, "companies": [{"id": "…", "name": "…"}, …]}`; for an adapter without the
   capability `{"supported": false, "partial": false, "companies": []}` (200, not an error: the console asks once per
   product and takes the answer as the shape of its field). At most 1000 entries, each id and name 1–128 characters, no
   duplicate id; any other shape is an invalid response to the console and it falls back to the text input. The source's
   failure is the catalogue's 503 (当前授权来源不可用), and the field falls back to text for that session.
4. **The console (S09-D).** With `supported`, the 公司范围 field is a picker: the companies by name, the id as secondary
   text (ADR 0014 decision 7), a filter when there are more than eight, the selection bounded by the query's limit of
   sixteen; the data chip names the companies, the query sends ids. With `partial`, a line 输入其他公司 ID… stays under the
   picker. Without the capability, the text input stays as it is. The embed page's query panel behaves the same. Nothing is
   remembered across sessions.
5. **The simulators implement it first.** The contract simulator's grant already holds the companies of each identity
   (`Grant.Companies`); it returns them with names (`company_demo` → 演示公司) and `Partial: false`, and `company_other`
   stays refused, so the console's refusal path and the manual's figure 10 remain testable through the typed line. A real
   product implements it when it is integrated; docs/REAL_SOURCE_ONBOARDING.md gains a row for the capability: whether
   the product can list the principal's companies with names, how current the list is, and what it does above 1000.
6. **Evidence.** S09-D measures the baseline before and after: T1's second typed text becomes a click and the count of
   texts falls from 5 to 4 (the only text left is the token and the tasks' restore id); clicks stay. The DOM suite adds the
   route's shapes (supported, unsupported, partial, over the bound, duplicate ids) and the picker's behaviour, and the
   real-browser script the picker at 1440 and 390.

## Consequences

An adapter that does not implement the capability is unchanged, and so is the console for it. The contract simulator and
the platform API gain a small, bounded, read-only route. Names cross the wire to the console and nowhere else; this is the
same class of disclosure as the catalogue's column labels, shown to a principal the source has already admitted to the
product. The onboarding checklist asks one more question of a real product. The picker's existence does not weaken the rule
that the source decides: a person can still choose a company the source will refuse (rights changed, or typed under
`partial`), and the refusal stays what it is today.

S09-D depends on this ADR, on `adapters/contractsim` implementing the capability and on the route; it is scheduled after
S09-G and S09-F, which are console-only (ADR 0014, "Order of the remaining work").
