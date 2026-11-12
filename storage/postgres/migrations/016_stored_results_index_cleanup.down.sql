-- 016 stored_results_index_cleanup, down: the index of 009 is built again, as 009 made it (CREATE INDEX blocks writes to assets while it
-- builds). Nothing of the platform has that name at this step, so an index that stands under it is someone's own: the step fails on it
-- and changes nothing, and the version it goes back to never has another index under 009's name.
CREATE INDEX assets_identity_kind_expires ON reporting_s01.assets (tenant, issuer, subject, kind, expires_at);
