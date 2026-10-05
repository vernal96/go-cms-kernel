# Core: API и обработчики

Ниже указаны полные внешние пути Core API с префиксом `/api`. Публичные маршруты выбирают сайт по домену запроса; management API требует аутентификацию и права. Path ID должны быть положительными числами. Общие правила и исключения описаны в [HTTP API](../../http-api.md).

## Публичные маршруты

| Метод и путь | Назначение |
| --- | --- |
| `GET /api/site` | Публичные данные текущего сайта |
| `GET /api/menu` | Меню текущего сайта |
| `GET /api/`, `GET /api/{resource-path}` | Публичный ресурс; способ ответа выбирает его тип |

Ресурс с путём `/about` читается через `GET /api/about`; в его данных, меню, поиске и SEO сохраняется путь `/about`. Внутренняя ссылка на ресурс перенаправляет API-клиента на `/api/{target-path}`, внешняя — на исходный внешний URL.

Публичные GET-маршруты не принимают query-параметры для выбора сайта. Переданный клиентом `site_id` не переключает runtime.

## Management API

### Сайты и профили

| Маршруты | Параметры |
| --- | --- |
| `GET /api/sites` | Query: `search`, `page`, `per_page`; pagination defaults: `1` и `10`, `per_page` максимум `100`. |
| `GET /api/sites/options` | Те же параметры плюс необязательный `exclude_id` — положительный ID сайта, исключаемого из вариантов. |
| `GET /api/site-profiles` | Параметров нет. |
| `POST /api/sites` | JSON: `profile_code`, `domain`, `locale`, `settings` (объект, может быть пустым), `is_public` (boolean). |
| `GET /api/sites/{siteID}`, `DELETE /api/sites/{siteID}` | `{siteID}` — положительный ID; body/query нет. |
| `PATCH /api/sites/{siteID}` | JSON: `profile_code`, `domain`, `locale`, `settings` (обязательно объект), `is_public` (обязательный boolean). Обновление задаёт состояние сайта целиком. |

Пример запроса списка: `GET /api/sites?search=example&page=1&per_page=20`.

### Ресурсы и дерево

| Маршруты | Параметры |
| --- | --- |
| `GET /api/sites/{siteID}/resources` | Необязательный query `parent_id`: положительный ID родителя; если не задан, возвращаются корневые ресурсы сайта. |
| `GET /api/sites/{siteID}/resources/metadata`, `GET /api/sites/{siteID}/resources/options` | Только `{siteID}` в пути; query/body нет. |
| `GET /api/sites/{siteID}/resources/lookup` | Query `search`, `page` (default `1`), `per_page` (default `10`, максимум `100`). |
| `POST /api/sites/{siteID}/resources` | JSON: `parent_id`, `type`, `template_code`, `content_type`, `content`, `target_resource_id`, `title`, `menu_title`, `slug`, `external_url`, `fields`, `type_settings`. `fields` и `type_settings` обязательны как объекты, в том числе пустые; остальные применяются в соответствии с типом ресурса. |
| `GET/PATCH/DELETE /api/sites/{siteID}/resources/{resourceID}` | ID сайта и ресурса в пути. PATCH принимает `expected_version`, `parent_id`, `type`, `template_code`, `image_media_id`, `title`, `menu_title`, `slug`, `annotation`, `content`, `content_type`, `target_resource_id`, `external_url`, `is_public`, `is_searchable`, `in_menu`, `in_sitemap`, `sort`, `published_at`, `unpublished_at`, `fields`, `type_settings`. `expected_version` положителен; `fields`, `type_settings`, все четыре boolean-флага и `sort` обязательны. Даты передаются в JSON формате RFC3339. |
| `POST /api/sites/{siteID}/resources/{resourceID}/move` | JSON: `parent_id` (null для корня), обязательные `position` (0 или больше), `expected_version` (положительный). |
| `POST /api/sites/{siteID}/resources/{resourceID}/transfer` | JSON: обязательные `target_site_id` и `expected_version`, оба положительные. |
| `DELETE /api/sites/{siteID}/resources/{resourceID}`, `DELETE /api/sites/{siteID}/resources/{resourceID}/permanent` | Мягкое / окончательное удаление; тело не требуется. |
| `POST /api/sites/{siteID}/resources/{resourceID}/restore` | JSON `with_descendants` (boolean): восстановить ли вместе потомков. |

Пример создания страницы:

```http
POST /api/sites/12/resources
Content-Type: application/json

{"type":"page","title":"О проекте","slug":"about","content_type":"html","content":"<p>Описание</p>","fields":{},"type_settings":{}}
```

Обновление ресурса всегда использует `expected_version`, возвращённую предыдущим чтением/изменением, чтобы обнаруживать конкурентные правки.

### Виджеты, версии и расширения ресурса

| Маршруты | Параметры |
| --- | --- |
| `POST /api/sites/{siteID}/resources/{resourceID}/widgets` | JSON: `code`, `area`, `view`, `columns`, `margin_top`, `margin_bottom`, `enabled`, `params` (обязательный объект), `param_bindings`, `expected_version` (обязательный положительный). |
| `PATCH /api/sites/{siteID}/resources/{resourceID}/widgets/{widgetID}` | Те же параметры представления, `params` и `enabled` обязательны; `expected_version` обязателен. `{widgetID}` — положительный ID привязки. |
| `DELETE /api/sites/{siteID}/resources/{resourceID}/widgets/{widgetID}` | JSON `expected_version` — положительная версия ресурса. |
| `PUT /api/sites/{siteID}/resources/{resourceID}/widgets/order` | JSON: `expected_version` и `items` (обязательный массив нового порядка). |
| `GET /api/sites/{siteID}/resources/{resourceID}/revisions` | `page`, `per_page`; defaults `1` и `10`, максимум `100`. |
| `GET /api/sites/{siteID}/resources/{resourceID}/revisions/{version}` | `{version}` — положительный номер ревизии. |
| `POST /api/sites/{siteID}/resources/{resourceID}/revisions/{version}/restore` | JSON `expected_version` — положительная текущая версия ресурса. |
| `DELETE /api/sites/{siteID}/resources/{resourceID}/revisions` | Purge ревизий этого ресурса; параметров запроса/тела нет. |
| `GET/PATCH /api/sites/{siteID}/resources/{resourceID}/extensions/{extensionCode}` | Код расширения — часть пути. PATCH передаёт JSON, форму которого определяет конкретное расширение. |
| `POST /api/sites/{siteID}/resources/{resourceID}/extensions/{extensionCode}/preview` | JSON с параметрами расширения для предпросмотра; схема также определяется расширением. |

### Элементы библиотеки

| Маршруты | Параметры |
| --- | --- |
| `GET /api/sites/{siteID}/resources/{libraryID}/items` | Query: `cursor` (opaque cursor следующей страницы), `limit` (default `25`), `search`, `filters`, `sort`. `filters` и `sort` — JSON-массивы, URL-encoded при передаче в query. |
| `POST /api/sites/{siteID}/resources/{libraryID}/items` | JSON: `image_media_id`, `template_code`, `title`, `slug`, `annotation`, `content`, `is_public`, `is_searchable`, `published_at`, `unpublished_at`, `fields` (обязательный объект). |
| `GET/PATCH/DELETE /api/sites/{siteID}/library-items/{itemID}` | `{itemID}` — ID элемента. PATCH использует поля создания плюс обязательные `expected_version`, `fields`, `is_public` и `is_searchable`. |
| `POST /api/sites/{siteID}/library-items/{itemID}/move` | JSON: `library_id` и `expected_version` — положительные ID/версия. |
| `POST /api/sites/{siteID}/library-items/{itemID}/restore`, `DELETE /api/sites/{siteID}/library-items/{itemID}/permanent` | Без тела; path ID определяет элемент. Обычный DELETE мягко удаляет. |

Допустимые значения `filters[].field`: встроенные имена `id`, `title`, `slug`, `template`, `is_public`, `is_searchable`, `published_at`, `created_at`, `updated_at`, либо валидный `resource.field.<key>`. `filters[]` принимает `field`, `operator`, `value`; `operator`: `eq`, `neq`, `in`, `not_in`, `gt`, `gte`, `lt`, `lte`. `sort[]` принимает `field` и `direction` (`asc` или `desc`).

Пример query (значения JSON в реальном URL нужно URL-encode):

```text
GET /api/sites/{siteID}/resources/{libraryID}/items?limit=10&search=report&filters=[{"field":"is_public","operator":"eq","value":true}]&sort=[{"field":"title","direction":"asc"}]
```

### Файлы и папки

| Маршрут | Параметры |
| --- | --- |
| `GET /api/files/disks` | Без параметров; возвращает доступные диски. |
| `GET /api/files/items` | Query `disk` обязателен; необязательный `folder_id` выбирает папку. |
| `GET /api/files/folders/resolve` | Query `disk` и `path` обязательны. |
| `POST /api/files/folders/ensure` | JSON `disk`, `path`; создать путь папок при необходимости. |
| `POST /api/files/folders` | JSON `disk`, `parent_id` (nullable), `name`. |
| `PATCH /api/files/folders/{folderID}`, `PATCH /api/files/{fileID}` | JSON `name`; path ID выбирает переименовываемую сущность. |
| `POST /api/files/uploads` | Multipart: обязательный `file`, `disk`, необязательный `folder_id`; размер ограничен конфигурацией. |
| `GET /api/files/{fileID}`, `GET /api/files/{fileID}/preview`, `GET /api/files/{fileID}/download` | Положительный `fileID`; query/body не требуются. |
| `POST /api/files/move` | JSON `disk`, `folder_id` (nullable), `items`: массив `{kind, id}`, где `kind` — `file` или `folder`. |
| `POST /api/files/delete-impact` | JSON `items`: массив `{kind, id}`; возвращает предварительный анализ последствий. |
| `POST /api/files/delete` | JSON `items`, `policy`, `impact_token`. Пустая policy означает безопасное удаление; подтверждённый media cascade требует policy `confirmed_media_cascade` и токен из анализа. |

### Изображения, media и site menu

| Маршрут | Параметры |
| --- | --- |
| `GET /api/files/{fileID}/thumbnail` | Query: `width`, `height`, `fit`, `position`, `profile`; каждый параметр можно передать один раз. Размеры — из набора `64`, `128`, `256`, `512`, `1024` и не выше лимита конфигурации; по умолчанию `128×128`. `fit`: `contain`, `cover`, `stretch`; `position`: `center`, `top`, `bottom`, `left`, `right`, `top-left`, `top-right`, `bottom-left`, `bottom-right`. `profile` выбирает профиль обработки. |
| `POST /api/media` | JSON `file_id` — положительный ID файла для создания media. |
| `GET /api/media/{mediaID}/image` | `{mediaID}` — положительный ID media; query/body нет. |
| `POST /api/media/{mediaID}/image` | JSON `expected_updated_at` (timestamp из image state) и `transform`. Transform поддерживает `crop` (`x`, `y`, `width`, `height`), `rotate` (кратно 90°, от −360 до 360), `scale_x`, `scale_y`, `width`, `height`, `fit`, `position`, `quality` (1–100). Непереданные scale по умолчанию 1, fit — `contain`, position — `center`, quality — 85. Версия защищает от перезаписи параллельных изменений. |
| `POST /api/media/{mediaID}/image/restore` | JSON `expected_updated_at` из текущего image state. |
| `GET/PUT /api/sites/{siteID}/media/{mediaID}/settings` | При GET необязательный query `code` выбирает схему настроек. PUT JSON: `code`, `values` (объект настроек), `expected_updated_at`. |
| `GET /api/sites/{siteID}/menu` | Только положительный `siteID`, без query/body. |

## Примеры

Получить корневые ресурсы сайта и запросить вторую страницу списка сайтов:

```http
GET /api/sites/12/resources
GET /api/sites?search=example&page=2&per_page=10
```

Неизвестные JSON-поля отклоняются. Управляющие операции, включая окончательное удаление и очистку истории, требуют соответствующих прав; для mutation API используйте актуальные версии сущностей, когда это поле предусмотрено контрактом.
