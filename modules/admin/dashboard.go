package admin

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

const dashboardSiteLimit = 10

type Dashboard struct {
	Sites     *DashboardSites     `json:"sites,omitempty"`
	Resources *DashboardResources `json:"resources,omitempty"`
	Users     *DashboardUsers     `json:"users,omitempty"`
	Groups    *DashboardGroups    `json:"groups,omitempty"`
}

type DashboardSites struct {
	Total   int             `json:"total"`
	Public  int             `json:"public"`
	Private int             `json:"private"`
	Items   []DashboardSite `json:"items"`
}

type DashboardSite struct {
	ID            site.ID `json:"id"`
	Name          string  `json:"name"`
	Domain        string  `json:"domain"`
	IsPublic      bool    `json:"is_public"`
	ResourceCount *int    `json:"resource_count,omitempty"`
}

type DashboardResources struct {
	Total int `json:"total"`
}

type DashboardUsers struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Blocked int `json:"blocked"`
}

type DashboardGroups struct {
	Total int `json:"total"`
}

func (m *Management) Dashboard(
	ctx context.Context,
	actor security.Actor,
) (Dashboard, error) {
	allowed, err := m.allowedPermissions(ctx, actor, []permission.Code{SiteReadPermission, ResourceReadPermission, UserReadPermission, GroupReadPermission})
	if err != nil {
		return Dashboard{}, err
	}
	canReadSites, canReadResources := allowed[SiteReadPermission], allowed[ResourceReadPermission]
	canReadUsers, canReadGroups := allowed[UserReadPermission], allowed[GroupReadPermission]
	var viewScope site.Scope
	if canReadSites {
		accessScope, err := m.policy.Scope(ctx, actor, SiteAccessView)
		if err != nil {
			return Dashboard{}, err
		}
		viewScope = site.Scope{
			All:     accessScope.All,
			SiteIDs: append([]site.ID(nil), accessScope.SiteIDs...),
		}
	}
	var editScope site.Scope
	if canReadResources {
		accessScope, err := m.policy.Scope(ctx, actor, SiteAccessEdit)
		if err != nil {
			return Dashboard{}, err
		}
		editScope = site.Scope{All: accessScope.All, SiteIDs: append([]site.ID(nil), accessScope.SiteIDs...)}
	}

	var sites *DashboardSites
	var resources *DashboardResources
	var users *DashboardUsers
	var groups *DashboardGroups
	tasks, ctx := errgroup.WithContext(ctx)
	tasks.SetLimit(3)
	if canReadSites || canReadResources {
		tasks.Go(func() error {
			var siteIDs []site.ID
			if canReadSites {
				repository, ok := m.repository.(site.StatisticsRepository)
				if !ok {
					return fmt.Errorf("site statistics repository is unavailable")
				}
				statistics, err := repository.Statistics(ctx, site.StatisticsQuery{
					Scope: viewScope,
					Limit: dashboardSiteLimit,
				})
				if err != nil {
					return fmt.Errorf("load dashboard site statistics: %w", err)
				}
				items := make([]DashboardSite, len(statistics.Items))
				siteIDs = make([]site.ID, len(statistics.Items))
				for index, item := range statistics.Items {
					items[index] = DashboardSite{
						ID:       item.ID,
						Name:     item.Name,
						Domain:   item.Domain,
						IsPublic: item.IsPublic,
					}
					siteIDs[index] = item.ID
				}
				sites = &DashboardSites{
					Total:   statistics.Total,
					Public:  statistics.Public,
					Private: statistics.Private,
					Items:   items,
				}
			}

			if canReadResources {
				repository, ok := m.resourceRepo.(resource.StatisticsRepository)
				if !ok {
					return fmt.Errorf("resource statistics repository is unavailable")
				}
				statistics, err := repository.Statistics(ctx, resource.StatisticsQuery{
					Scope:   editScope,
					SiteIDs: siteIDs,
				})
				if err != nil {
					return fmt.Errorf("load dashboard resource statistics: %w", err)
				}
				resources = &DashboardResources{Total: statistics.Total}
				if sites != nil {
					for index := range sites.Items {
						count := statistics.BySite[sites.Items[index].ID]
						sites.Items[index].ResourceCount = &count
					}
				}
			}

			return nil
		})
	}
	if canReadUsers {
		tasks.Go(func() error {
			repository, ok := m.userRepo.(user.StatisticsRepository)
			if !ok {
				return fmt.Errorf("user statistics repository is unavailable")
			}
			statistics, err := repository.Statistics(ctx)
			if err != nil {
				return fmt.Errorf("load dashboard user statistics: %w", err)
			}
			users = &DashboardUsers{
				Total:   statistics.Total,
				Active:  statistics.Active,
				Blocked: statistics.Blocked,
			}
			return nil
		})
	}
	if canReadGroups {
		tasks.Go(func() error {
			repository, ok := m.groupRepo.(group.StatisticsRepository)
			if !ok {
				return fmt.Errorf("group statistics repository is unavailable")
			}
			total, err := repository.Count(ctx)
			if err != nil {
				return fmt.Errorf("load dashboard group statistics: %w", err)
			}
			groups = &DashboardGroups{Total: total}
			return nil
		})
	}
	if err := tasks.Wait(); err != nil {
		return Dashboard{}, err
	}
	return Dashboard{Sites: sites, Resources: resources, Users: users, Groups: groups}, nil
}
