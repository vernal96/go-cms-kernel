# Admin: API и обработчики

Ниже указаны полные внешние пути Admin API. Handler монтируется на `/api/admin`; объявления внутри handler остаются относительными. Все маршруты требуют аутентификацию, management операции также проверяют права.

| Путь | Назначение |
| --- | --- |
| `GET /api/admin/session` | Информация о текущей сессии |
| `GET/PATCH /api/admin/profile` | Чтение и обновление своего профиля |
| `PUT /api/admin/profile/password`, `/api/admin/profile/preferences` | Пароль и пользовательские предпочтения |
| `/api/admin/profile/avatar` | Выбор, загрузка, удаление и preview аватара |
| `GET/POST /api/admin/users`, `GET/PATCH /api/admin/users/{userID}` | Список и CRUD пользователей |
| `/api/admin/users/{userID}/password`, `/api/admin/users/{userID}/groups`, `/api/admin/users/{userID}/block`, `/api/admin/users/{userID}/unblock` | Пароль, членство в группах и блокировка |
| `GET/POST /api/admin/groups`, `GET/PATCH/DELETE /api/admin/groups/{groupID}` | Управление группами |
| `GET /api/admin/groups/options`, `GET /api/admin/permission-catalog` | Варианты групп и каталог разрешений |
| `GET /api/admin/navigation`, `GET /api/admin/dashboard` | Динамическая навигация и dashboard |

## Параметры запросов

### Путь и query

- `{userID}` и `{groupID}` — положительные числовые ID.
- Для `GET /api/admin/users` поддерживаются `search`, `status`, `page`, `per_page`. `status`: `all`, `active` или `blocked`; по умолчанию `all`. `page` по умолчанию `1`, `per_page` — `10`, допустимый диапазон `per_page`: `1–100`.
- Для `GET /api/admin/groups` и `GET /api/admin/groups/options` поддерживаются `search`, `page`, `per_page` с теми же значениями пагинации.
- `GET /api/admin/navigation` принимает необязательный `site_id` — положительный ID сайта, для которого запрашивается доступная site-навигация. При неправильном значении вернётся `400`.
- Остальные GET-маршруты таблицы не принимают query-параметров.

### JSON-тела

- `PATCH /api/admin/profile`: `name`, `last_name`, `middle_name`, `phone`.
- `PUT /api/admin/profile/password`: `current_password`, `new_password`.
- `PUT /api/admin/profile/preferences`: `color_scheme` (`light`, `dark`, `system`) и `accent_color` (`blue`, `violet`, `indigo`, `emerald`, `amber`, `rose`).
- `PUT /api/admin/profile/avatar`: `file_id` — положительный ID файла. `POST /api/admin/profile/avatar/upload` принимает multipart-поле `file`; размер ограничен конфигурацией приложения. `DELETE /api/admin/profile/avatar` и `GET /api/admin/profile/avatar/preview` тела не имеют.
- `POST /api/admin/users`: `login`, `email`, `password`, `name`, необязательные `last_name`, `middle_name`, `phone`, `group_ids` (массив ID групп). `PATCH /api/admin/users/{userID}` принимает профильные поля без `password` и `group_ids`; пустые/невалидные значения проверяются сервисом.
- `PUT /api/admin/users/{userID}/password`: `password`. `PUT /api/admin/users/{userID}/groups`: `group_ids` — полный новый список ID групп (заменяет членство). Block/unblock тела не имеют.
- `POST /api/admin/groups`: `code`, `name`, `permission_codes` (массив кодов разрешений), `site_access` (массив объектов `{site_id, can_view, can_edit, can_delete}`). `PATCH /api/admin/groups/{groupID}` принимает `name`, `permission_codes`, `site_access`; отсутствие массива отличается от пустого массива: пустой массив очищает соответствующий список.

Пример создания пользователя:

```http
POST /api/admin/users
Content-Type: application/json

{"login":"editor","email":"editor@example.test","password":"<secret>","name":"Редактор","group_ids":[4]}
```

Список пользователей из предыдущего примера принимает `status=active` и пагинацию, например `GET /api/admin/users?search=editor&status=active&page=1&per_page=20`.

Списки возвращают пагинацию и набор разрешённых действий. Токен передаётся через `Authorization: Bearer <token>`; вход и выход выполняются через `POST /api/auth/login` и `POST /api/auth/logout`. JSON-декодер отклоняет неизвестные поля.
