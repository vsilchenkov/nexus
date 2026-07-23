-- §66: откат отображаемого имени пользователя.
ALTER TABLE users DROP COLUMN IF EXISTS name;
