UPDATE cluster_info
SET provider = 'self_hosted'
WHERE provider = '';

ALTER TABLE cluster_info
    ALTER COLUMN provider SET DEFAULT 'self_hosted';

ALTER TABLE cluster_info
    ADD CONSTRAINT chk_cluster_info_provider
    CHECK (provider IN (
        'kubernetes',
        'aliyun',
        'tencent',
        'huawei',
        'aws',
        'azure',
        'google',
        'other',
        'self_hosted'
    ));
