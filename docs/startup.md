# Архитектура запуска и конфигурация

```text
cmd/server → config → bootstrap → kernel App
                                  ├─ HTTP: migrations → prod seeds → Boot → server
                                  └─ console: kernel Console.Run → выбранная команда
```

`cmd/server` создаёт контекст с SIGINT/SIGTERM, выбирает режим, загружает настройки,
вызывает bootstrap и закрывает приложение после завершения. Ошибки, включая
ошибку закрытия, попадают в stderr и завершают процесс с ненулевым кодом.

## Ответственность пакетов

- `internal/config`: deployment-значения из ENV. `Load()` возвращает общие
  настройки, `LoadHTTP()` — настройки HTTP/JWT; консоль их не читает и не проверяет.
- `internal/infrastructure`: конкретные фабрики PostgreSQL, Redis, Kafka,
  public/private localstorage, Argon2id и database adapters модулей.
- `internal/settings`: проектные настройки `app.Definition`, logger factory,
  параметры загрузок и аватаров, регистрация проектного seed.
- `internal/profiles/<name>`: декларации профилей и bindings модулей. Bootstrap
  передаёт выбранный список профилей в `settings.Config`.
- `internal/platform`: проектная logger factory с созданием каталога для файла.
- `internal/bootstrap`: объединение деклараций и `app.New`, установка default
  `slog`, инициализация для выбранного режима и cleanup при ошибке.
- `internal/server`: JWT с session store приложения, kernel HTTP handler,
  mux с `/healthz`, listener и graceful shutdown через kernel server.
- Kernel `console` и Core commands: разбор и выполнение доменных команд.
  Проект передаёт аргументы и stdin/stdout/stderr в `App.Console().Run`.

Bootstrap возвращает `*app.App`, которым при успехе владеет вызывающий код.
Частичную сборку при ошибке `app.New` закрывает kernel. Последующие ошибки
инициализации закрывают приложение в bootstrap. Первичная ошибка и ошибка
закрытия объединяются через `errors.Join`.

## HTTP и консоль

Без аргументов `server` запускает HTTP: миграции, seeds с тегом `prod`, Boot и
HTTP runner. Системный seed Core `identity_shared` имеет теги `dev` и `prod`
и создаёт системные группы; проектный `starter` имеет только тег `dev`.
Обычный запуск не создаёт пользователей и демо-сайт.

`server console ...` только собирает приложение. Автоматического применения
миграций/seeds нет. Консоль вызывает Boot только для команд, которым нужны
доменные сервисы; команды схемы и seeds работают до Boot. Поэтому на новой базе
перед `users create` нужны миграции и системные seeds — например, обычный HTTP
запуск через `make up`, либо ручные команды:

```sh
server console migrations up
server console seeds up --tags=prod
```

Первый администратор создаётся вручную:

```sh
server console users create --login admin --email admin@example.test \
  --name Administrator --group admin --generate-password
```

Команда Core использует доменную валидацию, разрешает группу по коду и не
перезаписывает существующие аккаунты. Пароль можно передать одной строкой через
stdin вместо `--generate-password`. Проектная команда `bootstrap-admin` удалена;
специальные ENV для её параметров больше не используются.

Ручной запуск демо-данных:

```sh
server console seeds up --tags=dev
```

Команда применяет также общий seed Core. Проектный seed создаёт сайт `localhost`
и демо-пользователя. Seed history предотвращает повторное применение версии.
SQL остаётся embedded в `cmd/server`, файловая система передаётся в bootstrap
и `settings`. Переменной окружения для включения dev seeds больше нет.
Примеры Compose-команд находятся в [deployment](deployment.md).

## Переменные окружения

Ниже defaults самого Go-процесса; `.env.example` и Compose могут задавать другие
значения (например, `POSTGRES_HOST=postgres`, `JWT_ISSUER=go-cms-start`,
`JWT_AUDIENCE=go-cms-start-api`). `.env` создаётся скриптом `make env`, а не
автоматически читается приложением: для прямого запуска Go экспортируйте ENV.

| ENV | Default Go-процесса |
| --- | --- |
| `POSTGRES_HOST` | `localhost` |
| `POSTGRES_PORT` | `5432` |
| `POSTGRES_DB` | `cms` |
| `POSTGRES_USER` | `cms` |
| `POSTGRES_PASSWORD` | пустой |
| `POSTGRES_SSL_MODE` | `disable` |
| `REDIS_ADDR` | `localhost:6379` |
| `REDIS_PASSWORD` | пустой |
| `KAFKA_BROKERS` | `localhost:9092`; список через запятую |
| `FILES_PUBLIC_ROOT` | `var/files/public` |
| `FILES_PUBLIC_BASE_URL` | `http://localhost:8080` |
| `FILES_PRIVATE_ROOT` | `var/files/private` |
| `FILES_PRIVATE_BASE_URL` | `http://localhost:8080` |
| `FILES_PRIVATE_SIGNING_KEY` | нет; минимум 32 байта |
| `LOGGER_FILE_PATH` | `var/log/cms.log` |
| `SERVER_HOST` | `0.0.0.0` |
| `SERVER_PORT` | `8080` |
| `JWT_SIGNING_KEY` | нет; минимум 32 байта, только для HTTP |
| `JWT_ISSUER` | `go-cms` |
| `JWT_AUDIENCE` | `go-cms-api` |
| `JWT_ACCESS_TTL` | `30m` |
| `JWT_CLOCK_SKEW` | `30s` |

Пустые или состоящие из пробелов обычные значения используют defaults. Пароли
и signing keys сохраняются без обрезки пробелов и не выводятся в ошибках.
Порты должны быть целыми в диапазоне `1–65535`. Некорректные числа и длительности
теперь завершают запуск с ошибкой с именем ENV вместо молчаливого fallback.
TTL должен быть положительным, clock skew — от нуля до пяти минут и меньше TTL.
Ключ приватного диска нужен в обоих режимах; JWT-настройки нужны только HTTP.

Таймауты HTTP задаются значениями `ServerConfig`: чтение `5s`, запись `10s`,
остановка `5s`. Новые ENV для них не вводятся.

## Изменение композиции

Deployment-значения и их defaults меняйте в `config`, подключение конкретных
коннекторов/дисков и адаптеров — в `infrastructure`. Logger настраивается через
`settings` и `platform`. Профили/модули объявляются в `profile`, список профилей
подключается в bootstrap. Новый seed регистрируется в проектных декларациях с
явными тегами: `prod` применяется при HTTP-запуске, `dev` запускается вручную.
Bootstrap оркестрирует готовые пакеты; reusable kernel не импортирует проектный
`internal` и не знает ENV и deployment-политик этого проекта.
