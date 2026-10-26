-- Persistent synthetic source contract. Separate database from platform assets.
CREATE SCHEMA IF NOT EXISTS reporting_source_s01;
CREATE TABLE IF NOT EXISTS reporting_source_s01.profile (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), profile text NOT NULL, version integer NOT NULL
);
INSERT INTO reporting_source_s01.profile(singleton,profile,version) VALUES(true,'durable-contract-simulator-only',1) ON CONFLICT DO NOTHING;
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
