INSERT INTO iam_users (
    username,
    nickname,
    password,
    email,
    phone,
    avatar,
    is_admin,
    status,
    roles,
    created_at,
    updated_at
)
SELECT
    'admin',
    'admin',
    '$2a$12$ped6ldtK7wf2vUg8049AOe.8OxkwBnguQ0E4ttgpUlh.pESkt9fkq',
    '',
    '',
    '',
    TRUE,
    1,
    'admin',
    NOW(),
    NOW()
WHERE NOT EXISTS (
    SELECT 1
    FROM iam_users
    WHERE username = 'admin'
      AND deleted_at IS NULL
);
