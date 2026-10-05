# SEO: API и обработчики

У `seo` нет самостоятельного HTTP route prefix. Core management API вызывает его через resource extension:

| Метод и путь | Назначение |
| --- | --- |
| `GET /api/sites/{siteID}/resources/{resourceID}/extensions/seo` | Чтение метаданных для редактора |
| `PATCH /api/sites/{siteID}/resources/{resourceID}/extensions/seo` | Сохранение настроек |
| `POST /api/sites/{siteID}/resources/{resourceID}/extensions/seo/preview` | Preview вычисленных метаданных и warnings |

Path-параметры `siteID` и `resourceID` — положительные ID; код расширения фиксирован как `seo`, дополнительных query-параметров маршруты не принимают. `PATCH` и `POST /api/sites/{siteID}/resources/{resourceID}/extensions/seo/preview` принимают JSON с полями:

| Поле | Назначение |
| --- | --- |
| `title_template` | Шаблон заголовка |
| `description_template` | Шаблон описания |
| `keywords_template` | Шаблон ключевых слов |
| `canonical_template` | Шаблон canonical URL |
| `robots_index`, `robots_follow` | Флаги индексации и переходов |
| `og_title_template`, `og_description_template` | Шаблоны Open Graph |

Неизвестные ключи JSON отклоняются. Для preview передайте те же настройки, что при сохранении; ответ включает публичные данные, warnings и длину title/description.

При формировании публичной проекции ресурса SEO module предоставляет extension `seo` с title, description, keywords, canonical URL, robots и Open Graph данными. Шаблон или frontend включает эти данные в HTML; модуль сам не устанавливает HTTP-заголовки и не изменяет сайт-шаблон.
