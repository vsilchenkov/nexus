# syntax=docker/dockerfile:1.7
# Контекст сборки: корень репозитория (см. deploy/docker-compose.ha.yml).
#
# Балансировщик реплик (§93). Отдельный образ, а не голый nginx:alpine с
# bind-mount'ом конфига: конфиг и скрипт генерации upstream'ов должны ехать
# вместе с кодом и версионироваться им же. Bind-mount на боевом сервере
# означал бы «правка в git есть, а в контейнере старый файл» — ровно тот
# класс расхождений, из-за которого §93 вообще появился.
#
# Синтаксис конфига на этапе сборки не проверяется намеренно: `nginx -t`
# требует /etc/nginx/dyn/*.conf, а те генерируются при старте контейнера
# (10-nexus-upstreams.sh). Проверяет healthcheck в compose, и `nginx -t`
# внутри rolling.sh перед каждым reload.

FROM nginx:1.27-alpine

# Официальный образ выполняет /docker-entrypoint.d/*.sh перед стартом сервера —
# туда и кладём генератор списков реплик. Свой ENTRYPOINT не задаём: у образа
# он уже правильный (обработка шаблонов, graceful stop по SIGQUIT).
COPY deploy/docker/nginx.conf /etc/nginx/nginx.conf
COPY deploy/docker/nginx-upstreams.sh /docker-entrypoint.d/10-nexus-upstreams.sh

RUN chmod +x /docker-entrypoint.d/10-nexus-upstreams.sh

EXPOSE 8000 8080
