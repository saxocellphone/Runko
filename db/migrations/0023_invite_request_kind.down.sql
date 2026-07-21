-- Contact rows have no meaning under the pre-kind schema, and keeping
-- them could violate the restored one-live-per-address index; drop them
-- before collapsing back.
DROP INDEX idx_invite_requests_live_email;
DELETE FROM invite_requests WHERE kind = 'contact';
ALTER TABLE invite_requests DROP COLUMN kind;
DROP TYPE invite_request_kind;
CREATE UNIQUE INDEX idx_invite_requests_live_email
    ON invite_requests (lower(email))
    WHERE status IN ('pending', 'failed');
