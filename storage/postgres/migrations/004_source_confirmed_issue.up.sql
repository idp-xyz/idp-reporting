-- 004 source-confirmed issue (S03-H): the platform no longer numbers documents; the source product
-- gives every number (docs/ADR_0003_SOURCE_CONFIRMED_ISSUE.md), so the platform's counters go.
-- Refused (by the migrator, before this runs) while issue jobs or issue records exist: they are
-- S03-G's platform-numbered documents, never confirmed by a source product.
DROP TABLE reporting_s01.issue_numbers;
