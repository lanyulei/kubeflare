package domain

import "context"

type ListOptions struct {
	Keyword       string
	ProviderID    string
	RepositoryID  string
	ApplicationID string
	EnvironmentID string
	Status        string
	Limit         int
	Offset        int
}

type Repository interface {
	DashboardStats(ctx context.Context) (DashboardStats, error)

	ListProviders(ctx context.Context, opts ListOptions) ([]Provider, error)
	GetProvider(ctx context.Context, id string) (Provider, error)
	CreateProvider(ctx context.Context, provider Provider, audit Audit) (Provider, Audit, error)
	UpdateProvider(ctx context.Context, provider Provider, audit Audit) (Provider, Audit, error)
	DeleteProvider(ctx context.Context, id string, audit Audit) error

	ListGitRepositories(ctx context.Context, opts ListOptions) ([]GitRepository, error)
	GetGitRepository(ctx context.Context, id string) (GitRepository, error)
	CreateGitRepository(ctx context.Context, repository GitRepository, audit Audit) (GitRepository, Audit, error)
	UpdateGitRepository(ctx context.Context, repository GitRepository, audit Audit) (GitRepository, Audit, error)
	DeleteGitRepository(ctx context.Context, id string, audit Audit) error

	ListApplications(ctx context.Context, opts ListOptions) ([]Application, error)
	GetApplication(ctx context.Context, id string) (Application, error)
	CreateApplication(ctx context.Context, application Application, audit Audit) (Application, Audit, error)
	UpdateApplication(ctx context.Context, application Application, audit Audit) (Application, Audit, error)
	DeleteApplication(ctx context.Context, id string, audit Audit) error

	ListEnvironments(ctx context.Context, opts ListOptions) ([]Environment, error)
	GetEnvironment(ctx context.Context, id string) (Environment, error)
	CreateEnvironment(ctx context.Context, environment Environment, audit Audit) (Environment, Audit, error)
	UpdateEnvironment(ctx context.Context, environment Environment, audit Audit) (Environment, Audit, error)
	DeleteEnvironment(ctx context.Context, id string, audit Audit) error

	ListReleases(ctx context.Context, opts ListOptions) ([]Release, error)
	GetRelease(ctx context.Context, id string) (Release, error)
	CreateRelease(ctx context.Context, release Release, sync SyncRecord, audit Audit) (Release, SyncRecord, Audit, error)
	UpdateRelease(ctx context.Context, release Release, audits ...Audit) (Release, error)
	CreateReleaseApproval(ctx context.Context, approval ReleaseApproval, release Release, sync SyncRecord, audit Audit) (ReleaseApproval, Release, SyncRecord, Audit, error)

	ListSyncRecords(ctx context.Context, opts ListOptions) ([]SyncRecord, error)
	UpsertSyncRecord(ctx context.Context, sync SyncRecord) (SyncRecord, error)
	ListPolicyReports(ctx context.Context, opts ListOptions) ([]PolicyReport, error)
	CreatePolicyReport(ctx context.Context, report PolicyReport) (PolicyReport, error)
	ListAudits(ctx context.Context, opts ListOptions) ([]Audit, error)
	CreateAudit(ctx context.Context, audit Audit) (Audit, error)
}
