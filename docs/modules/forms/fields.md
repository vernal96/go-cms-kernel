# Forms: типы полей

Кроме общих типов [`core`](../core/fields.md), модуль добавляет три поля:

| Код | Назначение и параметры |
| --- | --- |
| `forms.captcha` | Проверка CAPTCHA. Опция `provider` выбирает зарегистрированный приложением provider. |
| `forms.consent` | Булево согласие; параметры задают текст и ссылку на документ. Обязательное согласие проверяется на сервере. |
| `forms.upload` | Multipart-файлы. Можно ограничить `mime_types`, `max_file_size`, `multiple` и `max_files`. |

В описании поля задаются `code`, `type`, `label`, `required`, `validators`, `options`, `show_on_site` и `show_in_results`. Upload и CAPTCHA имеют специальную обработку и не отображаются среди обычных значений публичной таблицы результатов.
