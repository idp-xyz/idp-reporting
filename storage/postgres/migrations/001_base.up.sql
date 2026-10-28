-- 001 base: the platform's own assets (S01-A). The profile row names the deployment kind and
-- carries the highest applied migration; it is inserted by the operator (init-profile), not here.
-- The schema itself is created by the migrator when absent (the first run needs CREATE on the
-- database); a later 001, after a down to zero, finds it in place.
CREATE TABLE reporting_s01.profile (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  profile text NOT NULL, version integer NOT NULL
);
CREATE TABLE reporting_s01.jobs (
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
CREATE INDEX jobs_claim ON reporting_s01.jobs(state,created_at);
CREATE TABLE reporting_s01.assets (
  id text PRIMARY KEY, tenant text NOT NULL, integration text NOT NULL,
  issuer text NOT NULL, subject text NOT NULL,
  kind text NOT NULL CHECK(kind IN ('snapshot','artifact')),
  data jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
