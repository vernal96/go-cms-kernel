// Package adminui defines optional, frontend-neutral admin UI capabilities.
package adminui

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/permission"
)

type NavigationScope string
type NavigationVisibility string

const (
	NavigationGlobal NavigationScope = "global"
	NavigationSite   NavigationScope = "site"
)

const NavigationAdministrator NavigationVisibility = "administrator"

var semanticCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

// NavigationItem is a declarative admin navigation definition. Route and Icon
// are semantic identifiers resolved by the compiled frontend application.
type NavigationItem struct {
	Code string
	// Parent attaches a top-level contribution to an existing root group at merge time.
	Parent     string
	Label      string
	Route      string
	Icon       string
	Order      int
	Permission permission.Code
	Visibility NavigationVisibility
	Scope      NavigationScope
	Children   []NavigationItem
}

// NavigationProvider is an optional capability. Module declarations use it
// for application-global contributions; final module runtimes use it for
// selected-site contributions.
type NavigationProvider interface {
	AdminNavigation() []NavigationItem
}

type PermissionValidator interface {
	Require(permission.Code) error
}

type Source struct {
	Code  string
	Items []NavigationItem
}

// Compile validates, clones and deterministically orders navigation sources.
// Item codes are unique across the complete compiled tree.
func Compile(
	sources []Source,
	scope NavigationScope,
	permissions PermissionValidator,
) ([]NavigationItem, error) {
	if scope != NavigationGlobal && scope != NavigationSite {
		return nil, fmt.Errorf("invalid navigation scope %q", scope)
	}

	result := make([]NavigationItem, 0)
	used := make(map[string]string)
	for index, source := range sources {
		if !semanticCodePattern.MatchString(source.Code) {
			return nil, fmt.Errorf(
				"navigation source at index %d has invalid code %q",
				index,
				source.Code,
			)
		}
		for itemIndex, item := range source.Items {
			compiled, err := compileItem(
				item,
				scope,
				permissions,
				used,
				source.Code,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"navigation source %q item at index %d: %w",
					source.Code,
					itemIndex,
					err,
				)
			}
			result = append(result, compiled)
		}
	}
	sortItems(result)
	return result, nil
}

func compileItem(
	item NavigationItem,
	scope NavigationScope,
	permissions PermissionValidator,
	used map[string]string,
	source string,
) (NavigationItem, error) {
	if !semanticCodePattern.MatchString(item.Code) {
		return NavigationItem{}, fmt.Errorf("invalid code %q", item.Code)
	}
	if owner, exists := used[item.Code]; exists {
		return NavigationItem{}, fmt.Errorf(
			"code %q is already contributed by %q",
			item.Code,
			owner,
		)
	}
	used[item.Code] = source

	if item.Parent != "" && !semanticCodePattern.MatchString(item.Parent) {
		return NavigationItem{}, fmt.Errorf("item %q has invalid parent %q", item.Code, item.Parent)
	}
	item.Label = strings.TrimSpace(item.Label)
	if item.Label == "" {
		return NavigationItem{}, errors.New("label is empty")
	}
	if item.Order < 0 {
		return NavigationItem{}, fmt.Errorf("item %q order is negative", item.Code)
	}
	if item.Scope != scope {
		return NavigationItem{}, fmt.Errorf(
			"item %q has scope %q, expected %q",
			item.Code,
			item.Scope,
			scope,
		)
	}
	if item.Icon != "" && !semanticCodePattern.MatchString(item.Icon) {
		return NavigationItem{}, fmt.Errorf(
			"item %q has invalid icon %q",
			item.Code,
			item.Icon,
		)
	}
	if item.Permission != "" {
		if _, err := permission.Parse(item.Permission); err != nil {
			return NavigationItem{}, fmt.Errorf(
				"item %q has invalid permission %q: %w",
				item.Code,
				item.Permission,
				err,
			)
		}
		if permissions != nil {
			if err := permissions.Require(item.Permission); err != nil {
				return NavigationItem{}, fmt.Errorf(
					"item %q permission %q is unavailable: %w",
					item.Code,
					item.Permission,
					err,
				)
			}
		}
	}
	if item.Visibility != "" && item.Visibility != NavigationAdministrator {
		return NavigationItem{}, fmt.Errorf("item %q has invalid visibility %q", item.Code, item.Visibility)
	}

	if len(item.Children) == 0 {
		if !semanticCodePattern.MatchString(item.Route) {
			return NavigationItem{}, fmt.Errorf(
				"leaf item %q has invalid route %q",
				item.Code,
				item.Route,
			)
		}
		item.Children = nil
		return item, nil
	}
	if item.Route != "" {
		return NavigationItem{}, fmt.Errorf(
			"group item %q must not declare a route",
			item.Code,
		)
	}

	children := make([]NavigationItem, 0, len(item.Children))
	for index, child := range item.Children {
		if child.Parent != "" {
			return NavigationItem{}, fmt.Errorf("nested item %q must not declare a parent", child.Code)
		}
		compiled, err := compileItem(
			child,
			scope,
			permissions,
			used,
			source,
		)
		if err != nil {
			return NavigationItem{}, fmt.Errorf(
				"item %q child at index %d: %w",
				item.Code,
				index,
				err,
			)
		}
		children = append(children, compiled)
	}
	sortItems(children)
	item.Children = children
	return item, nil
}

func sortItems(items []NavigationItem) {
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].Order == items[right].Order {
			return items[left].Code < items[right].Code
		}
		return items[left].Order < items[right].Order
	})
}

func Clone(items []NavigationItem) []NavigationItem {
	if items == nil {
		return nil
	}
	result := make([]NavigationItem, len(items))
	for index, item := range items {
		result[index] = item
		result[index].Children = Clone(item.Children)
	}
	return result
}

// Merge combines already-compiled navigation scopes, rejects cross-scope code
// collisions and applies the same deterministic top-level ordering.
func Merge(collections ...[]NavigationItem) ([]NavigationItem, error) {
	var result []NavigationItem
	used := make(map[string]struct{})
	for _, collection := range collections {
		cloned := Clone(collection)
		for _, item := range cloned {
			if err := collectCodes(item, used); err != nil {
				return nil, err
			}
		}
		result = append(result, cloned...)
	}
	roots := make(map[string]int)
	for index, item := range result {
		if item.Parent == "" {
			roots[item.Code] = index
		}
	}
	for _, item := range result {
		if item.Parent == "" {
			continue
		}
		index, exists := roots[item.Parent]
		if !exists || result[index].Route != "" {
			return nil, fmt.Errorf("navigation item %q parent %q is not a root group", item.Code, item.Parent)
		}
		item.Parent = ""
		result[index].Children = append(result[index].Children, item)
		sortItems(result[index].Children)
	}
	grouped := make([]NavigationItem, 0, len(roots))
	for _, item := range result {
		if item.Parent == "" {
			grouped = append(grouped, item)
		}
	}
	sortItems(grouped)
	return grouped, nil
}

func collectCodes(item NavigationItem, used map[string]struct{}) error {
	if _, exists := used[item.Code]; exists {
		return fmt.Errorf("navigation code %q is registered more than once", item.Code)
	}
	used[item.Code] = struct{}{}
	for _, child := range item.Children {
		if err := collectCodes(child, used); err != nil {
			return err
		}
	}
	return nil
}
