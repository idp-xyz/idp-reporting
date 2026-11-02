-- 005 retention (S04-B, docs/S04B_SCOPE.md B2): the expiry of a snapshot or an artifact becomes a column, so that the sweep finds what
-- has expired by an index instead of reading every payload. The payload stays the truth and keeps its digest (the column is not part of
-- it); the store checks that the two agree whenever it reads an asset. NULL for what never expires (the issue records, which the sweep
-- never deletes) and for a payload whose expiry could not be read. This step is never refused: whatever the rows hold, it runs.
ALTER TABLE reporting_s01.assets ADD COLUMN expires_at timestamptz;
DO $retention$
DECLARE
  one record;
BEGIN
  -- The payload's expiry is the RFC 3339 text Go wrote. PostgreSQL rounds a time with more than six fractional digits and the store's
  -- driver truncates it, so the text is cut to microseconds first: the column then equals what the store writes for the same time.
  BEGIN
    UPDATE reporting_s01.assets SET expires_at = regexp_replace(data->>'ExpiresAt', '(\.\d{6})\d+', '\1')::timestamptz
    WHERE kind IN ('snapshot','artifact') AND jsonb_typeof(data) = 'object'
      AND data->>'ExpiresAt' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$';
  EXCEPTION WHEN others THEN
    -- A value that looks like a time and is not (a month 13, a 30th of February) cannot be cast: the statement is undone, and the rows
    -- are tried one by one, leaving NULL, which the sweep never deletes, where the payload has no readable expiry.
    UPDATE reporting_s01.assets SET expires_at = NULL;
    FOR one IN SELECT id, regexp_replace(data->>'ExpiresAt', '(\.\d{6})\d+', '\1') AS text FROM reporting_s01.assets
               WHERE kind IN ('snapshot','artifact') AND jsonb_typeof(data) = 'object'
                 AND data->>'ExpiresAt' ~ '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$' LOOP
      BEGIN
        UPDATE reporting_s01.assets SET expires_at = one.text::timestamptz WHERE id = one.id;
      EXCEPTION WHEN others THEN
        NULL;
      END;
    END LOOP;
  END;
END
$retention$;
CREATE INDEX assets_expires_at ON reporting_s01.assets(expires_at);
