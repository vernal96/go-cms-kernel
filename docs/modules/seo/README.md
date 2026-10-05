# SEO

`seo` добавляет настройки SEO к редактору ресурсов `page` и `library` и выдаёт вычисленные метаданные в public resource extension. В Starter не включён.

Откройте [сущности и шаблоны](entities.md), [API интеграции](api.md) и [поля редактора](fields.md). Модуль реализует общий `resourceextension` contract; сам контракт не является самостоятельным модулем. `seo` не объявляет зависимости в `Dependencies()`.

Подключение: `seo.New(seo.Config{MaxTemplateLength: 2000})`. Поля `Config` принадлежат SEO; кэш и файловые привязки не принимаются.

Пошаговое подключение и зависимости: [руководство по модулям](../adding-modules.md).
