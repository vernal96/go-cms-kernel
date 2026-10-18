package kernel

import "fmt"

func (r *RuntimeRegistry) definitionCatalogs() []DefinitionRegistry {
	var catalogs []DefinitionRegistry
	for _, code := range r.definitionOrder {
		provider, ok := r.definitions[code].(DefinitionCatalogProvider)
		if !ok {
			continue
		}
		catalog := provider.DefinitionCatalog()
		if catalog != nil && !isNilValue(catalog) {
			catalogs = append(catalogs, catalog)
		}
	}
	return catalogs
}

func (r *RuntimeRegistry) snapshotDefinitionCatalogs() error {
	for _, code := range r.definitionOrder {
		provider, ok := r.definitions[code].(DefinitionCatalogProvider)
		if !ok {
			continue
		}
		catalog := provider.DefinitionCatalog()
		if catalog == nil || isNilValue(catalog) {
			return fmt.Errorf("module %q returned nil definition catalog", code)
		}
		for _, typeCode := range catalog.FieldTypes() {
			item, exists := catalog.FieldType(typeCode)
			if !exists || item == nil || isNilValue(item) || item.Code() != typeCode {
				return fmt.Errorf("module %q field type %q catalog is inconsistent", code, typeCode)
			}
			if err := r.addFieldType(item); err != nil {
				return fmt.Errorf("module %q: %w", code, err)
			}
		}
		for _, typeCode := range catalog.ValidatorTypes() {
			item, exists := catalog.ValidatorType(typeCode)
			if !exists || item == nil || isNilValue(item) || item.Code() != typeCode {
				return fmt.Errorf("module %q validator type %q catalog is inconsistent", code, typeCode)
			}
			if err := r.addValidatorType(item); err != nil {
				return fmt.Errorf("module %q: %w", code, err)
			}
		}
		for _, typeCode := range catalog.ResourceTypes() {
			item, exists := catalog.ResourceType(typeCode)
			if !exists || item == nil || isNilValue(item) || item.Code() != typeCode {
				return fmt.Errorf("module %q resource type %q catalog is inconsistent", code, typeCode)
			}
			if _, duplicate := r.resourceTypes[typeCode]; duplicate {
				return fmt.Errorf("module %q resource type %q already exists", code, typeCode)
			}
			// The owner has already compiled and validated the resource type.
			r.resourceTypes[typeCode] = item
		}
		for _, permissionCode := range catalog.Permissions() {
			item, exists := catalog.Permission(permissionCode)
			if !exists || item.Code != permissionCode {
				return fmt.Errorf("module %q permission %q catalog is inconsistent", code, permissionCode)
			}
			if err := r.addPermission(item); err != nil {
				return fmt.Errorf("module %q: %w", code, err)
			}
		}
	}
	r.catalogFrozen = true
	return nil
}
