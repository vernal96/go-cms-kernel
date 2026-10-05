# Развертывание проекта с нуля

Инструкция рассчитана на чистую рабочую копию и новую базу. Для Docker-сценария нужны Git, Python 3, GNU Make, Docker и Docker Compose v2 с поддержкой `--wait`; Go и Node.js на хосте не нужны.

## Backend

```sh
git clone https://github.com/vernal96/go-cms.git
cd go-cms
make up
```

`make up` создаёт `.env` из `.env.example`, генерирует уникальные секреты, собирает backend и запускает PostgreSQL, Redis и Kafka. Команда ждёт готовности сервисов. На новой базе применяются миграции и seeds с тегом `prod` (системные группы Core). Пользователи автоматически не создаются. Конфигурация и порядок запуска описаны в [архитектуре запуска](startup.md).

Проверьте `http://localhost:8080/healthz` — ожидается HTTP 200. Backend предоставляет [API под `/api`](http-api.md); корневой ресурс читается через `/api/`. Главная страница сайта автоматически не создаётся. Путь `/` не обслуживает API и возвращает 404.

Создайте первого администратора вручную через команду Core:

```sh
docker compose --env-file .env run --rm --no-deps server console users create \
  --login admin --email admin@example.test --name Administrator \
  --group admin --generate-password
```

Сохраните пароль из поля `generated_password` JSON-результата. Логин, email и имя задаются флагами. Команда использует обычный доменный сервис и не перезаписывает существующую учётную запись. Чтобы использовать свой пароль, уберите `--generate-password` и передайте его одной строкой через stdin:

```sh
printf '%s\n' "$ADMIN_PASSWORD" | docker compose --env-file .env run --rm --no-deps -T \
  server console users create --login admin --email admin@example.test \
  --name Administrator --group admin
```

`ADMIN_PASSWORD` здесь — переменная вашей оболочки для передачи stdin, а не настройка приложения.

Для изолированного локального демо примените dev seeds вручную:

```sh
docker compose --env-file .env run --rm --no-deps server console seeds up --tags=dev
```

Seed создаёт сайт `localhost` и пользователя `admin` с паролем, зафиксированным в SQL seed. Используйте его только для локального демо; пароль можно изменить в админке в разделе пользователей. Обычный запуск не применяет dev-only seeds. Для пустого демо-стенда достаточно dev seeds; отдельно создавать первого администратора на нём не нужно.

## Админка

В соседней директории клонируйте отдельное приложение:

```sh
cd ..
git clone https://github.com/vernal96/go-cms-admin.git
cd go-cms-admin
ADMIN_API_TARGET=http://host.docker.internal:8080 docker compose up -d --build --wait
```

Откройте `http://localhost:5173` и войдите созданной учётной записью администратора.

Без Docker админке нужны Node.js >=24 и npm:

```sh
cp .env.example .env
npm ci
npm run dev
```

Для локального режима укажите в `.env` `ADMIN_API_TARGET=http://localhost:8080`.

## Остановка и данные

```sh
make down   # остановить сервисы, сохранив БД и файлы
make up     # запустить снова
```

Не используйте `docker compose down -v`, если нужно сохранить данные: эта команда удаляет volumes текущего Compose project. Параметры портов и независимых копий проекта описаны в разделе «Порты и независимые копии» [README приложения Starter](https://github.com/vernal96/go-cms#порты-и-независимые-копии).
