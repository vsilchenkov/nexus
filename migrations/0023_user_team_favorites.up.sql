-- 0023_user_team_favorites.up.sql
--
-- §49 (избранные команды): персональный упорядоченный список избранных
-- команд пользователя для быстрого переключения из сайдбара SPA.
--
-- Составной FK на user_teams(user_id, team_id) вместо двух отдельных FK
-- на users/teams: один каскад покрывает сразу три события жизненного цикла
-- (удаление пользователя, удаление команды, исключение из членства — RemoveMember
-- удаляет строку user_teams напрямую) и заодно даёт БД-инвариант
-- «избранное ⊆ членство».
--
-- position — порядок в списке (drag-and-drop в UI). Дырки после каскадного
-- удаления терпимы: чтение сортирует по position, а запись всегда
-- перезаписывает список целиком с компактными 0..n-1 (PUT full-replace).
CREATE TABLE user_team_favorites (
    user_id    UUID        NOT NULL,
    team_id    UUID        NOT NULL,
    position   INT         NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, team_id),
    CONSTRAINT user_team_favorites_membership_fkey
        FOREIGN KEY (user_id, team_id)
        REFERENCES user_teams (user_id, team_id) ON DELETE CASCADE,
    CONSTRAINT user_team_favorites_position_nonneg CHECK (position >= 0)
);
