ALTER TABLE cluster_info
    DROP CONSTRAINT IF EXISTS chk_cluster_info_provider;

ALTER TABLE cluster_info
    ALTER COLUMN provider SET DEFAULT '';
