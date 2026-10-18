# Core: типы полей

Каталог `core.field` предоставляет стандартные типы для полей шаблонов, ресурсов и других модулей. Определение поля задаёт код, тип, подпись, обязательность, типизированные валидаторы и метаданные редактора.

| Код | Применение |
| --- | --- |
| `string`, `textarea`, `email`, `phone` | Текстовые значения; email проверяется как адрес, телефон имеет встроенную проверку E.164 |
| `int`, `float`, `checkbox` | Числа и логическое значение |
| `radio`, `select` | Выбор значения из `choices` |
| `media` | Ссылка на файл через отдельный объект Media |
| `json` | Структурированные JSON-значения |
| `repeater` | Повторяемые вложенные значения с дочерними определениями |

## Объявление полей в Go

Для новых объявлений стандартных полей используйте конструкторы пакета `field`: они сами задают стандартный `TypeCode` и принимают настройки нужного типа. Для `String`, `Integer`, `Float`, `Textarea`, `Email` и `Phone` настройки можно опустить либо передать одним аргументом; передача более одного аргумента вызывает panic. `Checkbox` и `JSON` дополнительных настроек не принимают. Остальные конструкторы требуют типизированные настройки:

| Конструктор | Настройки |
| --- | --- |
| `field.String`, `field.Textarea`, `field.Email` | `field.StringOptions` (`Multiple`) |
| `field.Integer` | `field.IntegerOptions` (`Multiple`, `Step *int64`) |
| `field.Float` | `field.FloatOptions` (`Multiple`, `Step *float64`) |
| `field.Checkbox`, `field.JSON` | Нет |
| `field.Radio` | `field.RadioOptions` (`Choices`) |
| `field.Select` | `field.SelectOptions` (`Choices`, `Multiple`) |
| `field.Phone` | `field.PhoneOptions` (`Multiple`) |
| `field.Media` | `field.MediaOptions` (`Disk`, `VirtualPath`, `SettingsCode`, `MIMETypes`, `Multiple`) |
| `field.Repeater` | `field.RepeaterOptions` (`Fields []field.Definition`) |

Например, обычные поля шаблона можно объявить так:

```go
fields := []field.Definition{
    field.String("title", "Заголовок").WithRequired(true),
    field.Textarea("summary", "Краткое описание").WithValidators(
        validation.MaxLength(255),
    ),
    field.Select("status", "Статус", field.SelectOptions{
        Choices: []field.Choice{
            {Value: "draft", Label: "Черновик"},
            {Value: "published", Label: "Опубликован"},
        },
    }),
}
```

Общие свойства задаются цепочкой методов, каждый из которых возвращает новое значение `Definition`: `WithRequired(bool)`, `WithValidators(...field.ValidatorDefinition)`, `WithEditor(field.EditorCode)`, `WithVisibleWhen(*field.VisibleWhen)` и `WithPublic(bool)`. `WithValidators` заменяет список валидаторов переданным списком. Это копирующие методы: исходное определение не меняется, а срезы, карта параметров и условие показа отделены от переданных значений. `WithPublic` относится к публичной проекции параметров профиля; для полей шаблона ресурсов он не включает публикацию.

Указанные типы аргументов позволяют компилятору Go отклонить, например, `field.Media("image", "Изображение", field.SelectOptions{})`. Конструкторы не компилируют схему и не заменяют runtime-проверки: Core по-прежнему проверяет настройки при компиляции схемы, а JSON/API-конфигурации и динамические типы проходят свои runtime-проверки. Если тип стандартный, но его литерал удобнее в конкретном контексте, существующий `field.Definition{...}` остаётся поддержан.

Для собственного типа сторонний модуль может объявить свои параметры и функцию-конструктор, возвращающую общий `field.Definition`; Kernel не требуется изменять:

```go
type ProductOptions struct {
    CategoryID int64
    Multiple   bool
}

func Product(key, label string, options ProductOptions) field.Definition {
    return field.Definition{
        Key: key, Label: label, Type: "catalog.product", Options: options,
    }
}
```

`field.ConfigField` — отдельное описание параметра конфигурации компонента для редактора. Оно содержит редакторские метаданные (`Key`, `Label`, `Type`, `Required`, `Validators`, `Options` и `Default`) и само по себе не является объявлением поля `field.Definition` или заменой конструктора стандартного типа.

Формат определений в JSON и HTTP API не меняется: там по-прежнему передаются код типа и сериализуемые `options`. Типизированные конструкторы относятся к удобному Go API; подробности runtime-валидации приведены в [контракте проверок полей](../../field-validation.md). Поля параметров сайта `Profile.Params` — отдельный контракт, описанный в [профилях](../../profiles.md).

## Файловое поле

`field.MediaOptions` содержит обязательные `Disk`, `VirtualPath` и `SettingsCode`, а также необязательные `MIMETypes` и `Multiple`:

```go
field.Media("gallery", "Галерея", field.MediaOptions{
        Disk: "public", VirtualPath: "site/news/gallery",
        SettingsCode: "news_gallery", Multiple: true,
        MIMETypes: []string{"image/*"},
    })
```

`Disk` должен быть объявленным диском, `VirtualPath` — допустимым виртуальным путём на нём, а `SettingsCode` — кодом схемы настроек медиа, объявленной в `core.Config.MediaSettings`. Компиляция профиля проверяет эти опции и наличие схемы. `MIMETypes` принимает MIME-типы и шаблоны наподобие `image/*`; сервер проверяет диск и фактический MIME каждого выбранного файла при сохранении. Интерфейс фильтрует неподходящие файлы до загрузки, но серверная проверка остаётся обязательной. `Multiple: false` хранит одно значение Media ID; `Multiple: true` — упорядоченный список таких ID.

Путь назначения влияет только на новые загрузки. Перед загрузкой интерфейс создаёт виртуальный путь на заданном диске, если он ещё не существует, и загружает файл туда. Открытый из поля файловый менеджер позволяет выбрать существующий файл из любого каталога того же диска. Это позволяет переиспользовать файл, не ограничивая выбор папкой загрузки. Пустые или неизвестные обязательные параметры, недопустимый виртуальный путь и неизвестный `SettingsCode` приводят к ошибке компиляции профиля; при сохранении backend отклоняет ссылку на другой диск или файл с MIME вне `MIMETypes` как ошибку валидации поля.

Схемы `MediaSettings` содержат только метаданные: поля со ссылками на сущности, включая `media` внутри повторителей и списков, отклоняются при компиляции. При смене текущего файла Media, редактировании изображения и восстановлении оригинала сервер повторно проверяет Disk/MIME по текущим схемам всех сохранённых Media-полей сайта, ресурса, виджета и модулей. Несовместимая смена отклоняется до изменения Media и очистки старого файла. Если один физический файл выбран в разных местах, для каждого выбора создаётся отдельная Media, поэтому редактор и метаданные одной плитки не меняют другие.

Результаты Forms используют ограничения файловых ссылок, сохранённые при отправке формы; изменение текущих полей формы не меняет этот исторический контракт.

Пример объявления поля и отдельного набора метаданных:

```go
import (
	kernel "github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

var Profile = kernel.Profile{
	Code: "editorial",
	Modules: []kernel.Module{
		core.New(core.Config{MediaSettings: []media.SettingsDefinition{{
			Code: "site_logo",
			Fields: []field.Definition{
				field.String("title", "Название"),
				field.String("alt", "Альтернативный текст"),
				field.Textarea("description", "Описание"),
			},
		}}}),
		admin.New(),
	},
	Params: []field.Definition{
		field.Media("logo", "Логотип", field.MediaOptions{
			Disk: "public", VirtualPath: "site/logo", SettingsCode: "site_logo",
			MIMETypes: []string{"image/*"},
		}).WithRequired(true),
	},
}
```

Если profile содержит `EditorTabs`, включите ключ `logo` ровно в один таб. Ключи `title`, `alt` и `description` сами по себе не имеют специального поведения: это обычные поля схемы. Значения схемы хранятся у конкретной Media отдельно от состояния изображения, поэтому настройки и редактирование изображения одной плитки не изменяют другую Media, даже если обе ссылаются на один исходный файл. Подробнее о GET/PUT настроек, валидации и конфликте версии — в [справочнике API Core](api.md#изображения-media-и-site-menu).

В админке одиночное и множественное Media-поля используют плитки без папок. Одиночная плитка занимает всю доступную ширину. В множественном режиме плитки можно переставлять перетаскиванием; порядок сохраняется как порядок массива Media ID. Клик по полю открывает файловый менеджер. Контекстное меню позволяет редактировать поддерживаемое изображение, открыть метаданные по `SettingsCode`, убрать значение из формы или запросить физическое удаление файла. «Удалить» меняет только черновик формы и сохраняется вместе с формой; «Удалить совсем» выполняется сразу после подтверждения и проверки всех ссылок. Аватар пользователя остаётся отдельным специализированным редактором. Контракт и состояния физического удаления описаны в [руководстве по окончательному удалению](file-field-deletion.md).

Скомпилированный валидатор модуля явно задаёт `Scope() field.ValidatorScope`: `ValidatorScopeValue` проверяет значение целиком, `ValidatorScopeItems` — каждый элемент множественного поля. Обязательные параметры проверок не принимают `null`; целые числа в `in`/`not_in` сохраняют точность при JSON-сериализации и загрузке настроек.

Подробный контракт, каталог и пример расширения модулем: [проверки полей в kernel](../../field-validation.md).
