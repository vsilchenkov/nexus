-- mssql up
CREATE TABLE users
(
    id INT IDENTITY(1,1) PRIMARY KEY,
    username NVARCHAR(255) NOT NULL,
    password_hash VARCHAR(72) NULL,  -- bcrypt: 60 символов + запас
    role VARCHAR(50) NOT NULL DEFAULT 'user',
    created_at DATETIME2(3) NOT NULL DEFAULT SYSDATETIME()
    
    -- UNIQUE constraint с явным именем
    CONSTRAINT uq_users_username UNIQUE (username),
    
    -- CHECK constraint для ролей
    CONSTRAINT check_user_role CHECK (role IN ('user', 'admin'))
);

-- Создание пользователя admin с NULL паролем
INSERT INTO users (username, password_hash, role) 
VALUES (N'admin', NULL, 'admin');
