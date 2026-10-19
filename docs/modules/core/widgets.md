# Core: виджеты

Core регистрирует шесть базовых контентных виджетов. Параметры задаются при добавлении widget binding к ресурсу; виджеты рендерятся в контексте текущего сайта и ресурса.

| Код | Назначение | Параметры |
| --- | --- | --- |
| `content` | Возвращает content текущего ресурса | нет |
| `html` | Редактируемый HTML-блок | `{"html":"<p>Привет!</p>"}` |
| `core_library_resources` | Ресурсы текущей библиотеки с cursor-пагинацией | `{"per_page":25}` |
| `core_library_mirror_resources` | Ресурсы текущего зеркала с локальными URL | `{"per_page":25}` |
| `resource_list` | Публичный список ресурсов с фильтрами, сортировкой и пагинацией | `{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}` |
| `core_resource_fields` | Выбранные поля шаблона текущего ресурса | `{"fields":["subtitle","cover"]}` |

Создание привязки выполняется через management API:

```http
POST /api/sites/12/resources/34/widgets
Content-Type: application/json

{"code":"resource_list","area":"main","view":"default","columns":12,"expected_version":1,"params":{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}}
```

В запросе используйте актуальную `version` ресурса как `expected_version`;
значение `1` в примерах подходит только для ресурса с текущей версией 1.

`resource_list` читает только публичные ресурсы текущего сайта. Параметр `fields` использует пути `resource.*`; пользовательские template fields указываются в пространстве `resource.field.<key>`. Доступные типы и области зависят от профиля/шаблона.

## Поля текущего ресурса

`core_resource_fields` возвращает значения выбранных полей шаблона ресурса,
для которого строится публичная страница. В настройке `fields` укажите массив
строковых ключей полей текущего шаблона. В админке список берётся из шаблона
ресурса; доступны поля любых типов, включая JSON, media и repeater. Виджет не
выбирает стандартные свойства ресурса вроде `title` или `content`.

Например, если шаблон содержит поля `subtitle` и `cover`, создайте привязку:

```http
POST /api/sites/12/resources/34/widgets
Content-Type: application/json

{"code":"core_resource_fields","area":"main","view":"default","columns":12,"expected_version":1,"params":{"fields":["subtitle","cover"]}}
```

Передайте в `expected_version` текущую версию ресурса, полученную через его
management API.

`fields` обязателен, должен содержать хотя бы один ключ, каждый ключ должен
существовать в шаблоне ресурса, а повторять ключи нельзя. Привязка параметра
`fields` к значению другого поля ресурса не поддерживается. Конфигурация
проверяется при создании и обновлении привязки, при смене шаблона ресурса и при
компиляции статического размещения виджета в шаблоне. Если проверка не проходит,
management API отклоняет запись с ошибкой валидации; публичный рендер отмечает
ошибку виджета кодом `invalid_params`.

В полном публичном ответе страница содержит объект `resource` и объект
`widgets`; результат виджета находится в `data.fields`. Имена и типы значений
сохраняются как в полях ресурса: строка остаётся строкой, JSON-объект — объектом,
массив repeater — массивом. Выбранное, но незаполненное поле включается со
значением `null`:

```json
{
  "resource": {
    "id": 34,
    "type": "page",
    "template": "article",
    "title": "Годовой отчёт",
    "path": "/reports/annual",
    "annotation": "",
    "content_type": null
  },
  "widgets": {
    "main": [{
      "key": "resource-widget-42",
      "code": "core_resource_fields",
      "view": "default",
      "columns": 12,
      "margin_top": 0,
      "margin_bottom": 0,
      "data": {
        "fields": {
          "subtitle": "Годовой отчёт",
          "cover": null
        }
      }
    }]
  }
}
```

Если поле удалили из шаблона после сохранения привязки, сохранённую конфигурацию
можно прочитать, чтобы отредактировать или удалить виджет. Любая новая запись,
содержащая устаревший ключ, будет отклонена; удалите ключ из `fields` или саму
привязку, затем сохраните исправленную конфигурацию.

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

Значения template-полей в `fields` проходят публичную проекцию по схеме шаблона.
Если выбранное поле имеет тип `media`, его ID заменяется объектом медиа,
описанным ниже; для поля `multiple` возвращается массив объектов с сохранением
порядка. Вложенные media-поля внутри `repeater` проецируются рекурсивно.
Свойство `image_media_id` в элементе списка — отдельное стандартное свойство
ресурса и по-прежнему содержит числовой ID или `null`.

## Контекст пользовательских виджетов

Core передаёт виджету нейтральные снимки текущего сайта и ресурса через
`widget.RenderInput`. `ResourceSnapshot` содержит `ID`, `Title`, `Content`,
`Path`, `PublishedAt`, `ImageMediaID` и `Fields` (поля шаблона ресурса). Эти
значения относятся к ресурсу, для которого строится ответ; не сохраняйте снимок
между запросами.

Модуль, которому для рендеринга нужно найти другие ресурсы, может получить
сервис запросов через `(*core.Runtime).ResourceQuery()`. Он создаётся в runtime
конкретного сайта и выполняет запросы с `context.Context`; для публичных данных
задавайте `PublicOnly: true` и `SiteID` текущего сайта. Например:

```go
package example

import (
    "context"
    "errors"

    "github.com/vernal96/go-cms-kernel/modules/core"
    "github.com/vernal96/go-cms-kernel/modules/core/resource"
    "github.com/vernal96/go-cms-kernel/modules/core/site"
    "github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func loadPublicResource(
    ctx context.Context,
    coreRuntime *core.Runtime,
    input widget.RenderInput,
    id resource.ID,
) (resource.Resource, error) {
    query := coreRuntime.ResourceQuery()
    if query == nil {
        return resource.Resource{}, errors.New("resource query service is unavailable")
    }
    page, err := query.Query(ctx, resource.Query{
        SiteID: site.ID(input.Site.ID),
        IDs: []resource.ID{id},
        Limit: 1,
        PublicOnly: true,
    })
    if err != nil {
        return resource.Resource{}, err
    }
    if len(page.Items) == 0 {
        return resource.Resource{}, resource.ErrNotFound
    }
    return page.Items[0], nil
}
```

Публичные настройки сайта формируются отдельно от снимка ресурса. Core
включает только определения `Profile.Params` с `Public: true`; для repeater
рекурсивно включает только публичные вложенные поля. Media-ссылка публикуется
как объект следующей формы:

```json
{
  "id": 42,
  "url": "/_cms/files/105",
  "mime_type": "image/jpeg",
  "size": 183204,
  "settings": {"alt": "Здание музея", "caption": "Главный вход"}
}
```

`id` — ID записи Media, `url` — относительный адрес доставки файла,
`mime_type` и `size` берутся из связанного файла. `settings` содержит
присутствующие значения полей схемы, указанной в `field.MediaOptions.SettingsCode`;
имена и типы значений соответствуют этой схеме. Для одиночного поля возвращается
объект, для `Multiple: true` — массив объектов в исходном порядке. То же
представление применяется к публичным template-полям ресурсов, выбранным полям
виджета `core_resource_fields`, спискам ресурсов и элементов библиотек, а также
к media-полям публичных виджетов. В `repeater` оно применяется рекурсивно.

Если запись Media или файл отсутствует либо недоступен в публичном файловом
диске, соответствующее значение равно `null`; элемент массива при этом
сохраняется. Другие ошибки разрешения прерывают проекцию. Управляющие API и
запись полей продолжают принимать ID Media: объект предназначен для публичного
чтения.

Для собственного публичного виджета с объявленными media-полями используйте
`(*core.Runtime).PublicFields().WidgetOutput`: метод преобразует значения только
для ключей, объявленных в переданном списке определений полей, и сохраняет прочие
вычисленные ключи ответа без изменений. Это не переписывает произвольные ID в
результате виджета.

```go
projector := coreRuntime.PublicFields()
output, err := projector.WidgetOutput(ctx, values, definition.Fields)
if err != nil {
    return nil, err
}
return output, nil
```

`definition.Fields` содержит определения полей публичного виджета с типом
`media` и корректными `MediaOptions`, включая `SettingsCode`; для вложенных полей
используйте repeater с такими же определениями.
