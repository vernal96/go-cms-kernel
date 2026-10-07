# Core: виджеты

Core регистрирует пять базовых контентных виджетов. Параметры задаются при добавлении widget binding к ресурсу; виджеты рендерятся в контексте текущего сайта и ресурса.

| Код | Назначение | Параметры |
| --- | --- | --- |
| `content` | Возвращает content текущего ресурса | нет |
| `html` | Редактируемый HTML-блок | `{"html":"<p>Привет!</p>"}` |
| `core_library_resources` | Ресурсы текущей библиотеки с cursor-пагинацией | `{"per_page":25}` |
| `core_library_mirror_resources` | Ресурсы текущего зеркала с локальными URL | `{"per_page":25}` |
| `resource_list` | Публичный список ресурсов с фильтрами, сортировкой и пагинацией | `{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}` |

Создание привязки выполняется через management API:

```http
POST /api/sites/12/resources/34/widgets
Content-Type: application/json

{"code":"resource_list","area":"main","view":"default","columns":12,"params":{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}}
```

`resource_list` читает только публичные ресурсы текущего сайта. Параметр `fields` использует пути `resource.*`; пользовательские template fields указываются в пространстве `resource.field.<key>`. Доступные типы и области зависят от профиля/шаблона.

## Постраничные списки ресурсов

`core_library_resources` размещается на библиотеке-источнике, `core_library_mirror_resources`
— на зеркале. Оба виджета используют один механизм запросов: только опубликованные
ресурсы, `per_page` от 1 до 100 (обязательно), необязательные JSON-массивы `filters`
и `sorting`. Встроенные пути полей имеют префикс `resource.`, пользовательские —
`resource.field.<key>`. Тип пользовательского поля определяется по шаблонам
источника; задавать `value_kind` не требуется. Сортировка имеет стабильный ID
как последний критерий. Коллекция не загружается целиком и не подсчитывается.

```json
{
  "per_page": 20,
  "filters": [{"field":"resource.id","operator":"gte","value":100}],
  "sorting": [{"field":"resource.published_at","direction":"desc"}]
}
```

`data` виджета в публичном ответе:

```json
{
  "resources": [{
    "id": 100,
    "title": "Заголовок ресурса",
    "annotation": "Краткое описание",
    "url": "/press/2026/10/example",
    "image_media_id": null,
    "published_at": "2026-10-06T10:00:00Z",
    "fields": {}
  }],
  "next_cursor": "opaque-cursor",
  "cursor_parameter": "cursor.resource-widget-42"
}
```

`cursor_parameter` вычисляется по ключу конкретного размещения виджета. Клиент
передаёт `next_cursor` в query-параметре с **точно этим именем**, URL-encoding
выполняется обычным URL API. Запрос идёт на тот же публичный ресурс, например
`GET /api/press?cursor.resource-widget-42=opaque-cursor`. Параллельные списки имеют
независимые параметры. Пустой `next_cursor` означает конец; для возврата назад
клиент хранит предыдущие курсоры. При изменении фильтров/сортировки курсор
сбрасывается. Отдельного публичного endpoint списка нет.

`url` первого виджета — путь на сайте библиотеки; второго — путь на сайте зеркала.
Ссылки не содержат `/api`: этот префикс добавляется только при запросе JSON.
Ошибки конфигурации или курсора возвращаются стандартной ошибкой конкретного
виджета в публичном ответе страницы.
