DROP INDEX IF EXISTS idx_gitops_release_project_mr_iid;
ALTER TABLE gitops_release DROP COLUMN IF EXISTS mr_iid;
ALTER TABLE gitops_release DROP COLUMN IF EXISTS project_id;
