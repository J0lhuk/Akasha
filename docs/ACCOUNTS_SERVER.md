# Учётные записи и PostgreSQL-сервер

В 0.2 self-hosted Compose использует PostgreSQL 17 / TimescaleDB 2.30.2 и Alembic. Приложение остаётся Go; Python запускается только в одноразовом контейнере миграции. Локальные проектные базы остаются SQLite и работают offline.

## Первый запуск сервера

Создайте `.env` по `.env.example`, заполните независимые случайные hex-пароли `AKASHA_DB_PASSWORD` и `AKASHA_APP_DB_PASSWORD`, recovery-token `AKASHA_HUB_TOKEN`, логин `AKASHA_ADMIN_USERNAME` и пароль `AKASHA_ADMIN_PASSWORD` длиной от 12 символов. Значения не добавляйте в Git/промпты. Затем:

```sh
docker compose up --build -d
docker compose ps
curl http://127.0.0.1:7342/healthz
```

DB health → Alembic upgrade → hub. Миграции сериализуются advisory lock; повторный upgrade идемпотентен. Приложение проверяет ожидаемую ревизию схемы перед запуском. `akasha_app` не superuser и не создаёт базы/роли; соединения ограничены 24. Миграционная роль отделена.

Compose не открывает PostgreSQL наружу. HTTP hub публикуется на loopback; для других компьютеров нужен HTTPS proxy как в [OPERATIONS](OPERATIONS.md). Пароль первого администратора используется только при создании аккаунта: изменение `.env` не сбрасывает существующий пароль или активные сессии.

## Логин и доступ команды

В примерах пароль заранее загружен безопасным способом в `AKASHA_ACCOUNT_PASSWORD`:

```powershell
akasha hub-login --url https://memory.example.com --username admin --pass-env AKASHA_ACCOUNT_PASSWORD --out D:\Protected\akasha-session.credential
akasha hub-account-add --url https://memory.example.com --hub-token-file D:\Protected\akasha-session.credential --name 'Developer 2' --username coder2 --pass-env AKASHA_NEW_USER_PASSWORD
akasha hub-grant --url https://memory.example.com --hub-token-file D:\Protected\akasha-session.credential --project atlas --user USER_ID --role editor
akasha hub-stats --url https://memory.example.com --hub-token-file D:\Protected\akasha-session.credential
```

`--out` для логина — новый файл. Windows защищает его DPAPI текущего пользователя, Unix — режимом 0600. Сессия живёт 12 часов, всего до 20 на пользователя. При истечении войдите снова и обновите credential file/настройку синхронизации. Токен не печатается в JSON логина. Совместимый `AKASHA_HUB_TOKEN` остаётся доступен автоматизации; recovery-token владельца храните отдельно.

`hub-account-set --user ID --username LOGIN --pass-env NAME` сбрасывает пароль и отзывает сессии. `hub-sessions-revoke --user ID` отзывает сессии; `--disable` также отключает аккаунт. Уже скачанные файлы и ключи остаются на устройствах. Пользователь с ключом проекта может расшифровать всё содержимое этого проекта; для разных аудиторий нужны разные проекты/ключи.

Локальный UI имеет свой каталог аккаунтов. Первый вход — recovery-token (`akasha token`), затем «Доступ команды» → «Задать мой логин и пароль», либо `akasha account-set --username LOGIN --pass-env NAME`. В дальнейшем форма использует логин/пароль и HttpOnly SameSiteStrict cookie. Пароли хранятся как Argon2id, sessions — только по хэшу. Есть rate limit и ограничение конкурентного хэширования. Локальный UI и серверные аккаунты разделены явно; регистрация на одном не создаёт аккаунт на другом.

## Хранение и backup

PostgreSQL содержит каталог/ACL, аккаунты/сессии и зашифрованные события. Append блокирует строку только соответствующего проекта; разные проекты записываются независимо, одинаковые события не дублируются. Timescale hypertable `request_metrics` хранит время, пользователя, нормализованный маршрут, status/latency/bytes; тексты промптов и секреты туда не попадают. Retention 90 дней применяется только к метрикам.

Для резервирования нужны **PostgreSQL backup плюс hub-data**, клиентские data-каталоги и отдельный recovery kit ключей. Используйте `pg_dump -Fc` или физический backup PostgreSQL с WAL и проверяйте восстановление в отдельный экземпляр. Копирование одного работающего PostgreSQL volume не считается согласованным backup. `docker compose down -v` удаляет данные и не используется при обновлении. Миграция назад требует заранее проверенного восстановления; destructive downgrade намеренно не автоматизирован.

`scripts/postgres-smoke.py` проверяет Alembic, nonsuperuser role, 10 аккаунтов, 400 параллельных запросов, общие записи, ACL, отзыв сессий, повтор миграции, restart и шифрованный перенос данных/skills. [Фактический результат](../reports/postgres-smoke.json). Это локальная Docker-проверка; удалённый production URL пока не настроен.

Источники: [Alembic](https://alembic.sqlalchemy.org/en/latest/tutorial.html), [блокировки PostgreSQL](https://www.postgresql.org/docs/current/explicit-locking.html), [соединения PostgreSQL](https://www.postgresql.org/docs/current/runtime-config-connection.html).
