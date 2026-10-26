# S03-B3 independent review

2026-10-27. Fixed baseline 5c5556d14b72d23f9a57ca8063be55d0fc0a33fa. Reviewed code 10d5e96756c2252cd3f740302151920c404e4ab7.
Standards PASS: no unresolved hard violation/substantive Fowler smell.
Spec PASS: no confirmed wrong/unsolicited behavior inside private publication scope.
Both axes independently ran layouts/httpapi tests and one targeted publication DOM flow.
P2 found: POST publication authorized v1 rather than selected target version, permitting
publication after target-only columns were removed. Fixed by exact target authorization.
Published reads check target columns; history/withdraw check current dataset binding so
removed old columns cannot prevent inspecting/withdrawing one's publication. Integration
and dataset revocation still deny metadata. Scope records this policy explicitly.
Regression changes catalog A/B and checks denied invalid target, valid target, removal,
history and withdrawal. Same test fails against old2872cab API via Go overlay, expected
negative-control log separate from current passing results.
Store tests cover draft/published separation, revision CAS, rollback publication,
withdrawal, owner privacy, event quota, reopen, old-file migration and corrupt null history.
Complete current Go normal/race and26 DOM are recorded separately from reviewers' targeted
runs. No real browser, PG rerun, production audit, sharing or approvals qualification.
