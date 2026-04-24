DROP TABLE IF EXISTS iam_external_identities;
DROP TABLE IF EXISTS iam_oidc_states;
DROP TABLE IF EXISTS iam_captcha_challenges;
DROP TABLE IF EXISTS iam_login_failures;
DROP TABLE IF EXISTS iam_refresh_tokens;
DROP TABLE IF EXISTS iam_revoked_tokens;
DROP TABLE IF EXISTS iam_auth_sessions;

ALTER TABLE IF EXISTS iam_users
    DROP COLUMN IF EXISTS mfa_secret,
    DROP COLUMN IF EXISTS mfa_enabled;
