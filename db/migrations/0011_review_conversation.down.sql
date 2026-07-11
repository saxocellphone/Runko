DROP TABLE IF EXISTS change_review_requests;
DROP INDEX IF EXISTS idx_change_comments_change;
ALTER TABLE change_comments DROP COLUMN IF EXISTS resolved;
ALTER TABLE change_comments DROP COLUMN IF EXISTS parent_id;
ALTER TABLE change_comments DROP COLUMN IF EXISTS side;
ALTER TABLE change_comments DROP COLUMN IF EXISTS head_sha;
