# Mail: API и обработчики

Site-management contribution монтируется на `/api/sites/{siteID}/mail`; оставшийся путь обрабатывает mail handler. Ниже указаны полные внешние адреса. Маршруты требуют аутентификацию и соответствующие права mail.

| Метод и путь | Назначение |
| --- | --- |
| `GET/POST /api/sites/{siteID}/mail/templates`, `GET/PATCH/DELETE /api/sites/{siteID}/mail/templates/{templateID}` | Список и CRUD шаблонов |
| `PATCH /api/sites/{siteID}/mail/templates/{templateID}/enabled` | Включить/выключить шаблон |
| `GET /api/sites/{siteID}/mail/variables` | Доступные site variables и параметры вложений |
| `GET /api/sites/{siteID}/mail/validator-types` | Каталог валидаторов текущего профиля для редактора переменных |
| `POST /api/sites/{siteID}/mail/preview` | Рендеринг без отправки |
| `POST /api/sites/{siteID}/mail/send` | Постановка отправки в очередь |
| `GET /api/sites/{siteID}/mail/send/templates` | Шаблоны для ручной отправки |
| `GET /api/sites/{siteID}/mail/messages`, `GET/DELETE /api/sites/{siteID}/mail/messages/{messageID}` | Журнал, детали и удаление сообщений |

Пример запроса:

```http
POST /api/sites/12/mail/send
Content-Type: application/json

{"template_id":4,"values":{"name":"Анна","recipient_email":"anna@example.test"}}
```

Успешное принятие запроса обычно отвечает `202 Accepted`; это подтверждает приём в очередь, не доставку письма. Переменные проверяются по схеме шаблона.

## Параметры запросов

`{templateID}` и `{messageID}` — положительные целые ID.

- `GET /api/sites/{siteID}/mail/templates` и `GET /api/sites/{siteID}/mail/send/templates`: `page` (по умолчанию `1`) и `per_page` (по умолчанию `20`, максимум `100`). Значения должны быть положительными целыми числами.
- `GET /api/sites/{siteID}/mail/messages`: та же пагинация и необязательные `status`, `template_code`, `recipient`, `date_from`, `date_to`. Даты должны быть RFC3339; `status` принимает значения состояния сообщения `queued`, `sending`, `retryable`, `accepted`, `failed`.
- `GET /api/sites/{siteID}/mail/variables`, `GET /api/sites/{siteID}/mail/validator-types`, `GET /api/sites/{siteID}/mail/templates/{templateID}`, `GET /api/sites/{siteID}/mail/messages/{messageID}` и DELETE не принимают query-параметров.

## Параметры тел запросов

- Создание и изменение шаблона принимают `code`, `name`, `enabled`, `from`, `to`, `cc`, `bcc`, `reply_to`, `subject`, `content_type`, `text_body`, `html_body`, `attachments`, `variables`. `from` и адресаты содержат `name` и `email`; `content_type` — `text` или `html`. Переменная задаётся ключом `key`, типом поля `type`, подписью `label`, флагом `required`, упорядоченным массивом `validators` (`{"type":"max_length","options":{"value":100}}`) и необязательными `options`.
- `PATCH /api/sites/{siteID}/mail/templates/{templateID}/enabled`: только флаг `enabled` (boolean).
- `POST /api/sites/{siteID}/mail/preview` и `POST /api/sites/{siteID}/mail/send`: `template_id` (положительный ID шаблона) и `values` (объект значений, ключи — коды переменных). Пример выше показывает отправку; тот же формат у preview.

Пример списка сообщений за период:

```http
GET /api/sites/12/mail/messages?status=failed&date_from=2026-09-01T00%3A00%3A00Z&page=1&per_page=20
```

Тело JSON декодируется строго: неизвестные поля отклоняются. Для получения схемы переменных и допустимых настроек вложений используйте `GET /api/sites/{siteID}/mail/variables`.
