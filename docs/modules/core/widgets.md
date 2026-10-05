# Core: виджеты

Core регистрирует три базовых контентных виджета. Параметры задаются при добавлении widget binding к ресурсу; виджеты рендерятся в контексте текущего сайта и ресурса.

| Код | Назначение | Параметры |
| --- | --- | --- |
| `content` | Возвращает content текущего ресурса | нет |
| `html` | Редактируемый HTML-блок | `{"html":"<p>Привет!</p>"}` |
| `resource_list` | Публичный список ресурсов с фильтрами, сортировкой и пагинацией | `{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}` |

Создание привязки выполняется через management API:

```http
POST /api/sites/12/resources/34/widgets
Content-Type: application/json

{"code":"resource_list","area":"main","view":"default","columns":12,"params":{"parent_mode":"root","limit":10,"fields":["resource.title","resource.path"]}}
```

`resource_list` читает только публичные ресурсы текущего сайта. Параметр `fields` использует пути `resource.*`; пользовательские template fields указываются в пространстве `resource.field.<key>`. Доступные типы и области зависят от профиля/шаблона.
