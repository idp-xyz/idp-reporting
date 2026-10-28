-- Persistent synthetic source contract. Separate database from platform assets.
-- Profile version 2 (S03-H) adds the number sequences and the issue reservations of the
-- document-authority capability. Running this file on a version 1 database upgrades it: the
-- missing tables are created, nothing else is touched and the profile moves to version 2. Open
-- runs it inside one transaction.
CREATE SCHEMA IF NOT EXISTS reporting_source_s01;
CREATE TABLE IF NOT EXISTS reporting_source_s01.profile (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), profile text NOT NULL, version integer NOT NULL
);
INSERT INTO reporting_source_s01.profile(singleton,profile,version) VALUES(true,'durable-contract-simulator-only',2) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS reporting_source_s01.versions (
 integration text NOT NULL, source_tenant text NOT NULL, id text NOT NULL,
 catalog jsonb NOT NULL, catalog_sha text NOT NULL,
 records jsonb NOT NULL CHECK(octet_length(records::text)<=2097152), records_sha text NOT NULL,
 PRIMARY KEY(integration,source_tenant,id)
);
CREATE TABLE IF NOT EXISTS reporting_source_s01.heads (
 integration text NOT NULL, source_tenant text NOT NULL, version_id text NOT NULL, unavailable boolean NOT NULL DEFAULT false,
 PRIMARY KEY(integration,source_tenant),
 FOREIGN KEY(integration,source_tenant,version_id) REFERENCES reporting_source_s01.versions(integration,source_tenant,id)
);
CREATE TABLE IF NOT EXISTS reporting_source_s01.grants (
 tenant text NOT NULL, integration text NOT NULL, issuer text NOT NULL, subject text NOT NULL, source_tenant text NOT NULL,
 active boolean NOT NULL, data jsonb NOT NULL CHECK(octet_length(data::text)<=65536), data_sha text NOT NULL,
 PRIMARY KEY(tenant,integration,issuer,subject,source_tenant)
);
CREATE TABLE IF NOT EXISTS reporting_source_s01.prepared (
 id text PRIMARY KEY, tenant text NOT NULL, integration text NOT NULL, issuer text NOT NULL, subject text NOT NULL, source_tenant text NOT NULL,
 version_id text NOT NULL, expires_at timestamptz NOT NULL,
 data jsonb NOT NULL CHECK(octet_length(data::text)<=131072), data_sha text NOT NULL,
 FOREIGN KEY(integration,source_tenant,version_id) REFERENCES reporting_source_s01.versions(integration,source_tenant,id)
);
-- One row per source (integration and source tenant): the prefix of its document numbers, the
-- last number it gave out (the row is locked to give the next one) and the test switch that
-- makes it refuse every document as not approved.
CREATE TABLE IF NOT EXISTS reporting_source_s01.issue_numbers (
 integration text NOT NULL, source_tenant text NOT NULL,
 prefix text NOT NULL CHECK(prefix ~ '^[A-Z][A-Z0-9]{0,7}$'),
 last_number bigint NOT NULL DEFAULT 0 CHECK(last_number>=0),
 refused boolean NOT NULL DEFAULT false,
 PRIMARY KEY(integration,source_tenant),
 FOREIGN KEY(integration,source_tenant) REFERENCES reporting_source_s01.heads(integration,source_tenant)
);
-- One row per issue job of a source, found by the platform's job id: reserved, then confirmed with
-- the file's digest and page count, or void with the platform's reason (a confirmed one can be
-- voided too and keeps its confirmation). A number is never reused: seq and number are unique.
CREATE TABLE IF NOT EXISTS reporting_source_s01.issues (
 integration text NOT NULL, source_tenant text NOT NULL, job_id text NOT NULL CHECK(job_id ~ '^[0-9a-f]{32}$'),
 ref text NOT NULL UNIQUE CHECK(ref ~ '^[0-9a-f]{32}$'),
 seq bigint NOT NULL CHECK(seq>=1), number text NOT NULL,
 state text NOT NULL CHECK(state IN ('reserved','confirmed','void')),
 issued_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 intent_sha text NOT NULL CHECK(intent_sha ~ '^[0-9a-f]{64}$'),
 sha256 text CHECK(sha256 ~ '^[0-9a-f]{64}$'), pages integer CHECK(pages BETWEEN 1 AND 100),
 confirmation_ref text CHECK(confirmation_ref ~ '^[0-9a-f]{32}$'), confirmed_at timestamptz,
 reason text CHECK(octet_length(reason)<=200), voided_at timestamptz,
 PRIMARY KEY(integration,source_tenant,job_id),
 UNIQUE(integration,source_tenant,seq), UNIQUE(integration,source_tenant,number),
 FOREIGN KEY(integration,source_tenant) REFERENCES reporting_source_s01.issue_numbers(integration,source_tenant),
 CHECK(CASE state
  WHEN 'reserved' THEN sha256 IS NULL AND pages IS NULL AND confirmation_ref IS NULL AND confirmed_at IS NULL AND reason IS NULL AND voided_at IS NULL
  WHEN 'confirmed' THEN sha256 IS NOT NULL AND pages IS NOT NULL AND confirmation_ref IS NOT NULL AND confirmed_at IS NOT NULL AND reason IS NULL AND voided_at IS NULL
  ELSE reason IS NOT NULL AND voided_at IS NOT NULL AND (sha256 IS NULL) = (pages IS NULL) AND (sha256 IS NULL) = (confirmation_ref IS NULL) AND (sha256 IS NULL) = (confirmed_at IS NULL) END)
);
UPDATE reporting_source_s01.profile SET version=2 WHERE singleton AND profile='durable-contract-simulator-only' AND version=1;
