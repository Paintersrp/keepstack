-- +goose Up
INSERT INTO users (id, email, password_hash)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'dev@example.com',
    '$2a$12$w29oCFhGu3E7yBRLBXjg5eQqr8RP4eAbOeXtfLOcAfeUeawuO/HEa'
)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM users
WHERE id = '00000000-0000-0000-0000-000000000001';
