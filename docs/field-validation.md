# Field validation

A `field.Definition` declares a value type, optional `Required`, and ordered `Validators []field.ValidatorDefinition`. The field type handles normalization and intrinsic identity checks (for example email syntax, E.164 phone, integer and JSON shape). Configurable validators add field-specific constraints. `Required` remains schema-owned and is not a validator code.

```go
import (
    "github.com/vernal96/go-cms-kernel/modules/core/field"
    "github.com/vernal96/go-cms-kernel/modules/core/field/validation"
)

var slug = field.Definition{
    Key: "slug", Type: field.TypeString, Label: "Slug",
    Validators: []field.ValidatorDefinition{
        validation.MinLength(2),
        validation.MaxLength(100),
        validation.Regex(`^[a-z0-9-]+$`),
    },
}
```

`field.Schema.Compile` resolves each validator from the site runtime registry, decodes its options, checks compatibility and compiles it once. Unknown codes, duplicate codes, invalid options, invalid scopes and nil compiled validators fail schema compilation. Required validator options reject both missing keys and `null`; an explicit numeric zero remains valid where the validator permits it. At validation time the schema checks presence/`Required`, normalizes, checks emptiness and intrinsic type constraints, then runs configured validators in declaration order. Applicable failures accumulate as `{key, code, params}`; repeater and list paths include indices such as `items[2].title`. The backend is authoritative.

## Core catalog

| Value | Codes | Options |
| --- | --- | --- |
| Numeric | `min`, `max`, `between`, `multiple_of` | `value` or `min` and `max` numbers |
| Integer digits | `digits`, `min_digits`, `max_digits`, `digits_between` | nonnegative counts |
| String length | `min_length`, `max_length`, `length`, `length_between` | Unicode character counts |
| String characters and case | `alpha`, `alpha_dash`, `alpha_numeric`, `ascii`, `lowercase`, `uppercase` | none |
| String content | `starts_with`, `ends_with`, `doesnt_start_with`, `doesnt_end_with`, `regex`, `not_regex` | `value` string |
| Membership | `in`, `not_in` | `values` array of typed scalar values |
| String or list content | `contains`, `doesnt_contain` | `values` array; substring for scalar strings, element membership for lists |
| List cardinality | `min_items`, `max_items`, `items_between`, `items_count`, `unique_items` | counts, or none for uniqueness |
| Formats | `url`, `ip`, `ipv4`, `ipv6`, `mac`, `uuid`, `ulid`, `hex_color` | none |
| Boolean | `accepted`, `declined` | none; strict normalized boolean |

`validation` provides typed constructors for the complete catalog. Numeric bounds apply to numbers only; string length uses separate codes. `in` and `not_in` normalize configured values to the field storage kind. Lists apply scalar validators to each element and list validators to the whole list. Go `regexp` syntax uses RE2; PHP delimiters and PREG features such as lookbehind are unsupported.

`Multiple` is a structural field option. Cardinality is expressed by `min_items` and `max_items`. Phone pattern restrictions use `regex` in addition to the intrinsic E.164 format. Numeric `Step` remains editor metadata. These replacements are breaking changes to pre-production field declarations and persisted Forms schema; recreate development Forms data after updating the migration.

## Module contribution and admin

A module returns its validator types in `kernel.ModuleRegistry.ValidatorTypes`. The runtime registers them only for profiles enabling that module. It rejects nil and duplicate codes, snapshots presentation metadata, and exposes `ValidatorType(code)` and `ValidatorTypes()`. A custom type implements `Code()` and `Compile(field.ValidatorContext, any) (field.Validator, error)`; the compiled validator implements `Scope() field.ValidatorScope` and `Validate(any) error`. Check the context storage kind, field type and multiplicity during compilation. Return `field.ValidationErrors` with stable codes and structured params when a value fails.

`ValidatorScopeValue` passes the entire normalized field value, including a whole list or repeater. `ValidatorScopeItems` passes each normalized list element and prefixes failures with its index; it requires `ValidatorContext.Multiple == true`. Compilation always receives the context of the complete field. A validator supporting both scalar values and individual list elements returns `ValidatorScopeItems` for multiple fields and `ValidatorScopeValue` otherwise. Scope is explicit for every validator, including module contributions; it is not inferred from its code or presentation metadata.

```go
func (v distinctDomainValidator) Scope() field.ValidatorScope {
    return field.ValidatorScopeValue // Validate receives the complete email list.
}
```

Implement `field.ValidatorMetadataProvider` to supply the label, `[]field.ConfigField` options, `FieldTypes`/`Multiple` or `Applicability` restrictions, and optional `OptionsEditor`. Admin metadata is exposed in Forms editor data and Mail's validator catalog endpoint. The admin builder uses this metadata to add, configure, reorder and remove validators; custom option editors use the existing editor registry. Validator definitions remain JSON objects such as:

```json
{"type":"max_length","options":{"value":100}}
```

Custom options must remain JSON serializable. `field.CloneValidatorDefinitions` detaches option objects, including nested repeater definitions. Generic numeric options are decoded as `json.Number` to preserve integer membership values throughout cloning, HTTP and persistence; custom compilers can use `DecodeRequiredOptions` with typed target fields. Forms persists the JSON configuration and compiles it with the current site's registry on reconstruction.

Business and contextual checks such as persistence `unique`/`exists`, password checks, cross-field comparisons, conditional presence and network `active_url` belong to later schema or application layers. File and media policies remain with their field types and storage services.
