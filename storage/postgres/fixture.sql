-- S01-A contract-simulator fixture only. Not a production migration or role plan.
CREATE SCHEMA IF NOT EXISTS reporting_s01;
CREATE TABLE IF NOT EXISTS reporting_s01.profile (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  profile text NOT NULL, version integer NOT NULL
);
INSERT INTO reporting_s01.profile(singleton,profile,version)
VALUES(true,'contract-simulator-only',1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS reporting_s01.jobs (
  id text PRIMARY KEY, tenant text NOT NULL, integration text NOT NULL,
  issuer text NOT NULL, subject text NOT NULL, kind text NOT NULL CHECK(kind IN ('snapshot','export')),
  state text NOT NULL CHECK(state IN ('queued','running','succeeded','failed','cancelled')),
  idem_key text NOT NULL, request_hash text NOT NULL, generation bigint NOT NULL DEFAULT 0,
  lease_until timestamptz, source_expires_at timestamptz NOT NULL,
  snapshot_id text NOT NULL DEFAULT '', artifact_id text NOT NULL DEFAULT '',
  error_code text NOT NULL DEFAULT '', data jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(tenant,integration,issuer,subject,kind,idem_key)
);
CREATE INDEX IF NOT EXISTS jobs_claim ON reporting_s01.jobs(state,created_at);
CREATE TABLE IF NOT EXISTS reporting_s01.assets (
  id text PRIMARY KEY, tenant text NOT NULL, integration text NOT NULL,
  issuer text NOT NULL, subject text NOT NULL,
  kind text NOT NULL CHECK(kind IN ('snapshot','artifact')),
  data jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
