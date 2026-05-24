-- postgres
CREATE TABLE users
(
	id SERIAL PRIMARY KEY,
	username VARCHAR(255) UNIQUE NOT NULL,
	password_hash VARCHAR(72) NULL,
	role VARCHAR(50) NOT NULL DEFAULT 'user',
	created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Опционально: CHECK constraint для ролей
ALTER TABLE users ADD CONSTRAINT check_user_role 
    CHECK (role IN ('user', 'admin'));

-- Создание пользователя admin с NULL паролем
INSERT INTO users (username, password_hash, role) 
VALUES ('admin', NULL, 'admin');