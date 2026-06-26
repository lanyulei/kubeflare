-- 为发布单增加 GitLab 项目数字 ID 与 MR IID,使 MR webhook 可用稳健的 (project,iid)
-- 结构化主键关联发布单,取代易受尾斜杠/反代/主机名差异影响的 MR web 地址字符串匹配。
ALTER TABLE gitops_release ADD COLUMN IF NOT EXISTS project_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE gitops_release ADD COLUMN IF NOT EXISTS mr_iid INTEGER NOT NULL DEFAULT 0;

-- (project_id, mr_iid) 联合索引加速 webhook 关联查询。
CREATE INDEX IF NOT EXISTS idx_gitops_release_project_mr_iid
    ON gitops_release(project_id, mr_iid)
    WHERE deleted_at IS NULL;
