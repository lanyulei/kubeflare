-- 把 gitops_sync_record 从"事件流水"收敛为"每个发布单一条当前同步态"。
-- 先去重(保留每个 release_id 最新的一条,按 updated_at 降序),再建唯一索引,
-- 使 ON CONFLICT(release_id) 的真正 upsert 成为可能。release_id 为空的历史记录
-- (无发布单关联)不纳入唯一约束。

-- 去重:删除每个 release_id 下除最新一条外的所有软删未删记录。
DELETE FROM gitops_sync_record a
USING gitops_sync_record b
WHERE a.release_id <> ''
  AND a.release_id = b.release_id
  AND a.deleted_at IS NULL
  AND b.deleted_at IS NULL
  AND (
    a.updated_at < b.updated_at
    OR (a.updated_at = b.updated_at AND a.id < b.id)
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_gitops_sync_record_release_active
    ON gitops_sync_record(release_id)
    WHERE deleted_at IS NULL AND release_id <> '';
