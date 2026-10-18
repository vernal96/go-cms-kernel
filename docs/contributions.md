# Contributions модулей

Механизм contributions позволяет модулю объявить собственную типизированную точку регистрации, а зависимому модулю — добавить в неё реализацию на нужном этапе сборки. Kernel определяет порядок и проверяет зависимости; типы расширений, реестры и проверки принадлежат модулю-владельцу. Регистрации не хранятся в глобальном изменяемом реестре.

Используются два этапа:

| Вид | Когда выполняется | Для чего подходит |
| --- | --- | --- |
| Definition contribution | При компиляции профиля, до создания SiteRuntime | Типы полей, валидаторы и другие определения, которые должны быть известны компилятору схем |
| Runtime contribution | Для каждого SiteRuntime после создания runtime-модулей | Обработчики и реализации, которым нужны сервисы конкретного сайта; например, действия и элементы Forms |

## Definition contributions

Модуль-владелец реализует `kernel.DefinitionProvider` и возвращает новый объект `kernel.ModuleDefinitions` для каждой компиляции профиля. Этот объект владеет регистрациями и обязан проверить их и закрыть при `FinalizeDefinitionBuild`. Kernel сохраняет завершённые определения в неизменяемом снимке профиля; сайты этого профиля могут разделять его. Затем модуль-контрибьютор реализует необязательный `kernel.DefinitionContributionProvider`. `kernel.DefineFor[T]` принимает узкий типизированный контракт владельца, хотя инфраструктурный интерфейс `Apply` получает `kernel.ModuleDefinitions`.

Например, сторонний модуль может добавить поле и валидатор в Core. У Core есть конкретный тип определений с `RegisterFieldType` и `RegisterValidatorType`:

```go
package customfields

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type module struct{}

func (module) Code() kernel.ModuleCode { return "custom.fields" }
func (module) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{core.ModuleCode} }

func (module) Validate(context.Context, kernel.ModuleValidationContext) error { return nil }
func (module) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return runtime{}, nil
}

func (module) DefinitionContributions() []kernel.DefinitionContribution {
	return []kernel.DefinitionContribution{
		kernel.DefineFor[*core.Definitions](core.ModuleCode, func(ctx context.Context, definitions *core.Definitions) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := definitions.RegisterFieldType(scoreType{}); err != nil {
				return err
			}
			return definitions.RegisterValidatorType(scoreValidatorType{})
		}),
	}
}

type runtime struct{}

func (runtime) ModuleCode() kernel.ModuleCode { return "custom.fields" }

type scoreType struct{}

func (scoreType) Code() field.TypeCode { return "custom.score" }

func (scoreType) Compile(_ field.CompileContext, options any) (field.ValueType, error) {
	var config struct { Min int64 `json:"min"`; Max int64 `json:"max"` }
	if err := field.DecodeRequiredOptions(options, &config, "min", "max"); err != nil { return nil, err }
	if config.Min > config.Max { return nil, errors.New("min must not exceed max") }
	return scoreValue{min: config.Min, max: config.Max}, nil
}

type scoreValue struct { min, max int64 }

func (v scoreValue) Normalize(value any) (any, error) {
	switch value := value.(type) {
	case int: return int64(value), nil
	case int64: return value, nil
	case json.Number: return value.Int64()
	default: return nil, fmt.Errorf("expected integer, got %T", value)
	}
}
func (v scoreValue) Empty(value any) bool { return value == nil }
func (v scoreValue) Validate(value any) error {
	number, ok := value.(int64)
	if !ok || number < v.min || number > v.max { return fmt.Errorf("score must be between %d and %d", v.min, v.max) }
	return nil
}
func (v scoreValue) Example() any { return v.min }
func (scoreValue) StorageKind() field.StorageKind { return field.StorageInteger }
func (scoreValue) Multiple() bool { return false }

type scoreValidatorType struct{}

func (scoreValidatorType) Code() field.ValidatorCode { return "custom.score_range" }
func (scoreValidatorType) Compile(ctx field.ValidatorContext, options any) (field.Validator, error) {
	if ctx.Storage != field.StorageInteger || ctx.Multiple { return nil, errors.New("custom.score_range requires a scalar integer field") }
	var config struct { Min int64 `json:"min"`; Max int64 `json:"max"` }
	if err := field.DecodeRequiredOptions(options, &config, "min", "max"); err != nil { return nil, err }
	if config.Min > config.Max { return nil, errors.New("min must not exceed max") }
	return scoreValidator{min: config.Min, max: config.Max}, nil
}

type scoreValidator struct { min, max int64 }
func (scoreValidator) Scope() field.ValidatorScope { return field.ValidatorScopeValue }
func (v scoreValidator) Validate(value any) error {
	number, ok := value.(int64)
	if !ok || number < v.min || number > v.max { return fmt.Errorf("value is outside configured range") }
	return nil
}
```

Фрагмент выше показывает модуль, который только вносит определения в Core; он не является их владельцем и поэтому не реализует `DefinitionProvider`. Интерфейс `field.Type` состоит из `Code` и `Compile`; компилятор вызывает `Compile` с конфигурацией определения. Возвращённый `field.ValueType` нормализует, проверяет и описывает пример значения; для участия в типизированном хранении здесь также реализован `field.StorageValueType`. `field.ValidatorType` компилирует конфигурацию в `field.Validator`, который задаёт область проверки через `Scope` и реализует `Validate`. Для параметров редактора реализуйте соответствующие metadata-интерфейсы из пакета `field`.

Поле использует `TypeCode`, а настройки типа и валидатора остаются JSON-сериализуемыми данными:

```go
field.Definition{
	Key: "score", Type: "custom.score", Label: "Оценка",
	Options: map[string]any{"min": 0, "max": 100},
	Validators: []field.ValidatorDefinition{{
		Type: "custom.score_range", Options: map[string]any{"min": 10, "max": 90},
	}},
}
```

Объявляйте это определение в поле шаблона ресурса; схема скомпилируется только для профиля, где присутствуют Core и `custom.fields`. Профиль без поставщика завершит разрешение `custom.score` ошибкой. Код остаётся ссылкой на зарегистрированный тип, а не объектом Go.

Добавьте модуль в профиль после Core. При сборке Kernel проверит наличие target-модуля, явную зависимость и порядок. Регистрация неизвестного или повторного кода, ошибка конфигурации или ошибка callback останавливает компиляцию с контекстом профиля и модулей. Компиляция определений выполняется при создании приложения и может завершиться ошибкой ещё в `App.New`, до `Boot`. После финализации Core отклоняет новые регистрации; владельцы обязаны закрыть все свои изменяемые реестры в `FinalizeDefinitionBuild`.

Схемы хранят тип как типизированный `field.TypeCode` в `field.Definition.Type`. Компилятор разрешает код через каталог определений профиля. Код в шаблоне или ресурсе — ссылка на декларацию, а не сериализованный Go-объект `field.Type` или `ValueType`. Когда тип отсутствует в профиле, разрешение/компиляция схемы завершается ошибкой. Поэтому сайт без `custom.fields` не получает это расширение. Типы и валидаторы должны быть известны до компиляции шаблонов и схем, а не регистрироваться при HTTP-запросе.

## Runtime contributions: Forms

Runtime contributions нужны, когда расширению необходим runtime целевого модуля. Провайдером является runtime исходного модуля: он реализует `kernel.RuntimeContributionProvider` и возвращает вклады для этого создания SiteRuntime. Создавайте runtime и contribution значения заново при каждом `Build`; не сохраняйте сайт-зависимые объекты в глобальном состоянии или разделяемом profile definition. `kernel.ContributeTo[T]` сужает target до интерфейса, объявленного модулем-владельцем. Kernel создаёт runtime модулей в порядке зависимостей, проверяет target и зависимость, применяет вклады и вызывает доступные финализаторы до публикации SiteRuntime.

Forms предоставляет `ActionRegistrar` и `ElementRegistrar`. Runtime Forms реализует оба контракта и закрывает реестры в `FinalizeRuntimeBuild`. Пример стороннего модуля, зависящего от Forms:

```go
package customforms

import (
	"context"
	"encoding/json"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/forms"
)

type customFormsModule struct{}

func (customFormsModule) Code() kernel.ModuleCode { return "custom.forms" }
func (customFormsModule) Dependencies() []kernel.ModuleCode { return []kernel.ModuleCode{forms.ModuleCode} }
func (customFormsModule) Validate(context.Context, kernel.ModuleValidationContext) error { return nil }

func (customFormsModule) Build(context.Context, kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	return customFormsRuntime{}, nil
}

type formsExtensionTarget interface {
	kernel.ModuleRuntime
	forms.ActionRegistrar
	forms.ElementRegistrar
}

type customFormsRuntime struct{}

func (customFormsRuntime) ModuleCode() kernel.ModuleCode { return "custom.forms" }
func (customFormsRuntime) RuntimeContributions() []kernel.RuntimeContribution {
	return []kernel.RuntimeContribution{
		kernel.ContributeTo[formsExtensionTarget](forms.ModuleCode, func(_ context.Context, target formsExtensionTarget) error {
			return target.RegisterActionType(customAction{})
		}),
		kernel.ContributeTo[formsExtensionTarget](forms.ModuleCode, func(_ context.Context, target formsExtensionTarget) error {
			return target.RegisterElementType(customElement{})
		}),
	}
}

type customAction struct{}
func (customAction) Code() string { return "custom.notify" }
func (customAction) Metadata() forms.ActionTypeMetadata {
	return forms.ActionTypeMetadata{Code: "custom.notify", Label: "Уведомить", Available: true}
}
func (customAction) ValidateConfig(context.Context, forms.ActionValidationContext, json.RawMessage) error { return nil }
func (customAction) Execute(context.Context, forms.ActionExecutionContext, json.RawMessage) (forms.ActionExecutionResult, error) {
	return forms.ActionExecutionResult{}, nil
}

type customElement struct{}
func (customElement) Code() forms.ElementTypeCode { return "custom.notice" }
func (customElement) Metadata() forms.ElementTypeMetadata {
	return forms.ElementTypeMetadata{Code: "custom.notice", Label: "Уведомление"}
}
func (customElement) ValidateConfig(json.RawMessage) error { return nil }
```

Контракт действия включает `Code`, `Metadata`, `ValidateConfig` и `Execute`; элемент включает `Code`, `Metadata` и `ValidateConfig`. В рабочем модуле добавьте реальную бизнес-логику в эти реализации. Ошибка регистрации, недопустимый или дублирующийся код, отсутствующий target либо отсутствие явной зависимости прерывают создание runtime. После финализации Forms запрещает регистрацию. Каждое создание SiteRuntime получает собственные runtime и реестры, поэтому вклад не переносится между сайтами; модуль-поставщик должен присутствовать в профиле сайта.

## Существующие интерфейсы регистрации

Не каждый интерфейс `Provider` является contribution. Например, `widget.Provider` и `adminui.NavigationProvider` описывают отдельные контракты получения данных/описаний и не заменяются этим механизмом автоматически. `DependencyProvider` объявляет граф зависимостей, а `RuntimeBuildFinalizer` — lifecycle hook, а не источник расширений.

`RegistryProvider` и `kernel.ModuleRegistry` пока остаются поддерживаемым декларативным API и используются как fallback, когда модуль не реализует `DefinitionProvider`. Если реализованы оба интерфейса, компилятор берёт определения через `DefinitionProvider`. Часть встроенных деклараций и потребителей `RegistryProvider` всё ещё существует. При разработке нового реестра отдавайте предпочтение контракту, принадлежащему модулю-владельцу, и не добавляйте новые типизированные свойства в `ModuleRegistry` без необходимости совместимости с существующими потребителями. Сам по себе `RegistryProvider` не выполняет роль точки runtime-расширения.

См. также [руководство по созданию модулей](modules/adding-modules.md), [поля Core](modules/core/fields.md), [типы полей Forms](modules/forms/fields.md) и [справочник модулей](modules/README.md).
