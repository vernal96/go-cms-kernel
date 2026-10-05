# SEO: сущности и шаблоны

Metadata привязаны к паре сайт/ресурс и хранят шаблоны title, description, keywords, canonical URL, Open Graph title/description, а также `robots_index` и `robots_follow`. Применимо к типам `page` и `library`.

Пример настроек:

```json
{
  "title_template": "{{ resource.title }} — Example",
  "description_template": "{{ resource.annotation }}",
  "robots_index": true,
  "robots_follow": true,
  "og_title_template": "{{ resource.title }}"
}
```

Модуль поддерживает переменные ресурса `resource.title`, `resource.menu_title`, `resource.annotation`, `resource.slug`, `resource.path`, скалярные site params и не-file поля шаблона. Это интерполяция разрешённых переменных, не выполнение произвольного кода.
