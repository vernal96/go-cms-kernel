package admin

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type Navigation struct {
	Items []NavigationItem `json:"items"`
}

type NavigationItem struct {
	Code     string                  `json:"code"`
	Label    string                  `json:"label"`
	Route    string                  `json:"route,omitempty"`
	Icon     string                  `json:"icon,omitempty"`
	Order    int                     `json:"order"`
	Scope    adminui.NavigationScope `json:"scope"`
	Children []NavigationItem        `json:"children,omitempty"`
}

type navigationComposer struct {
	global        []adminui.NavigationItem
	authorizer    security.Authorizer
	permissions   adminui.PermissionValidator
	administrator interface {
		IsAdministrator(context.Context, security.Actor) (bool, error)
	}
}

type moduleNavigation struct {
	provided bool
	items    []adminui.NavigationItem
}

func newNavigationComposer(
	profiles []kernel.Profile,
	authorizer security.Authorizer,
	permissions adminui.PermissionValidator,
	administrators ...interface {
		IsAdministrator(context.Context, security.Actor) (bool, error)
	},
) (*navigationComposer, error) {
	if authorizer == nil {
		return nil, errors.New("admin navigation authorizer is nil")
	}
	if permissions == nil {
		return nil, errors.New("admin navigation permission catalog is nil")
	}
	var administrator interface {
		IsAdministrator(context.Context, security.Actor) (bool, error)
	} = denyAdministrator{}
	if len(administrators) > 0 && administrators[0] != nil {
		administrator = administrators[0]
	}

	seen := make(map[kernel.ModuleCode]moduleNavigation)
	sources := make([]adminui.Source, 0)
	for _, profile := range profiles {
		for _, profileModule := range profile.Modules {
			if profileModule.Module == nil {
				continue
			}
			moduleCode := profileModule.Module.Code()
			provider, provided := profileModule.Module.(adminui.NavigationProvider)
			var items []adminui.NavigationItem
			if provided {
				items = adminui.Clone(provider.AdminNavigation())
			}

			if previous, exists := seen[moduleCode]; exists {
				if previous.provided != provided ||
					(provided && !reflect.DeepEqual(previous.items, items)) {
					return nil, fmt.Errorf(
						"module %q has inconsistent global admin navigation across profiles",
						moduleCode,
					)
				}
				continue
			}
			seen[moduleCode] = moduleNavigation{
				provided: provided,
				items:    items,
			}
			if provided {
				sources = append(sources, adminui.Source{
					Code:  string(moduleCode),
					Items: items,
				})
			}
		}
	}

	global, err := adminui.Compile(
		sources,
		adminui.NavigationGlobal,
		permissions,
	)
	if err != nil {
		return nil, fmt.Errorf("compile global admin navigation: %w", err)
	}
	return &navigationComposer{
		global:        global,
		authorizer:    authorizer,
		permissions:   permissions,
		administrator: administrator,
	}, nil
}

type denyAdministrator struct{}

func (denyAdministrator) IsAdministrator(context.Context, security.Actor) (bool, error) {
	return false, nil
}

func (c *navigationComposer) compose(
	ctx context.Context,
	actor security.Actor,
	runtime *site.Runtime,
) ([]adminui.NavigationItem, error) {
	if c == nil {
		return nil, errors.New("admin navigation composer is unavailable")
	}

	items, err := c.compile(runtime)
	if err != nil {
		return nil, err
	}
	return c.filter(ctx, actor, items)
}

// compile is independent of the current actor. Validate all contributed items,
// including hidden ones, before accepting a candidate site runtime.
func (c *navigationComposer) compile(runtime *site.Runtime) ([]adminui.NavigationItem, error) {
	var siteItems []adminui.NavigationItem
	if runtime != nil {
		profileRuntime := runtime.Profile()
		if profileRuntime == nil {
			return nil, errors.New("selected site profile runtime is unavailable")
		}
		sources := make([]adminui.Source, 0)
		for _, moduleRuntime := range profileRuntime.Modules() {
			provider, ok := moduleRuntime.(adminui.NavigationProvider)
			if !ok {
				continue
			}
			sources = append(sources, adminui.Source{
				Code:  string(moduleRuntime.ModuleCode()),
				Items: provider.AdminNavigation(),
			})
		}
		compiled, err := adminui.Compile(
			sources,
			adminui.NavigationSite,
			c.permissions,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"compile admin navigation for site %d: %w",
				runtime.Site().ID,
				err,
			)
		}
		siteItems = compiled
	}

	items, err := adminui.Merge(c.global, siteItems)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// PrepareRuntimes validates navigation before the site catalog persists or
// publishes any candidate. Permission filtering remains request-scoped.
func (m *Management) PrepareRuntimes(ctx context.Context, plan site.RuntimePlan) (site.RuntimePreparation, error) {
	for _, runtime := range plan.Next() {
		if err := ctx.Err(); err != nil {
			return site.RuntimePreparation{}, err
		}
		if _, err := m.navigation.compile(runtime); err != nil {
			return site.RuntimePreparation{}, err
		}
	}
	return site.RuntimePreparation{}, nil
}

func (c *navigationComposer) filter(
	ctx context.Context,
	actor security.Actor,
	items []adminui.NavigationItem,
) ([]adminui.NavigationItem, error) {
	result := make([]adminui.NavigationItem, 0, len(items))
	for _, item := range items {
		if item.Visibility == adminui.NavigationAdministrator {
			allowed, err := c.administrator.IsAdministrator(ctx, actor)
			if err != nil {
				if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) {
					continue
				}
				return nil, err
			}
			if !allowed {
				continue
			}
		}
		if item.Permission != "" {
			err := c.authorizer.Check(ctx, actor, item.Permission)
			switch {
			case err == nil:
			case errors.Is(err, security.ErrForbidden):
				continue
			default:
				return nil, fmt.Errorf(
					"check admin navigation permission %q: %w",
					item.Permission,
					err,
				)
			}
		}

		children, err := c.filter(ctx, actor, item.Children)
		if err != nil {
			return nil, err
		}
		if len(item.Children) > 0 && len(children) == 0 {
			continue
		}
		item.Children = children
		result = append(result, item)
	}
	return result, nil
}

func navigationDTO(items []adminui.NavigationItem) []NavigationItem {
	result := make([]NavigationItem, len(items))
	for index, item := range items {
		result[index] = NavigationItem{
			Code:     item.Code,
			Label:    item.Label,
			Route:    item.Route,
			Icon:     item.Icon,
			Order:    item.Order,
			Scope:    item.Scope,
			Children: navigationDTO(item.Children),
		}
	}
	return result
}
