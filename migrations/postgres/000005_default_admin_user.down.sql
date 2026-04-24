DELETE FROM iam_users
WHERE username = 'admin'
  AND password = '$2a$12$ped6ldtK7wf2vUg8049AOe.8OxkwBnguQ0E4ttgpUlh.pESkt9fkq'
  AND is_admin = TRUE
  AND roles = 'admin';
