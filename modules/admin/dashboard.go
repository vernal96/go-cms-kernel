package admin

import (
	"context"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
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
	canReadSites, err := m.allowed(ctx, actor, SiteReadPermission)
	if err != nil {
		return Dashboard{}, err
	}
	canReadResources, err := m.allowed(ctx, actor, ResourceReadPermission)
	if err != nil {
		return Dashboard{}, err
	}
	canReadUsers, err := m.allowed(ctx, actor, UserReadPermission)
	if err != nil {
		return Dashboard{}, err
	}
	canReadGroups, err := m.allowed(ctx, actor, GroupReadPermission)
	if err != nil {
		return Dashboard{}, err
	}

	var result Dashboard
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

	var siteIDs []site.ID
	if canReadSites {
		repository, ok := m.repository.(site.StatisticsRepository)
		if !ok {
			return Dashboard{}, fmt.Errorf("site statistics repository is unavailable")
		}
		statistics, err := repository.Statistics(ctx, site.StatisticsQuery{
			Scope: viewScope,
			Limit: dashboardSiteLimit,
		})
		if err != nil {
			return Dashboard{}, fmt.Errorf("load dashboard site statistics: %w", err)
		}
		items := make([]DashboardSite, len(statistics.Items))
		siteIDs = make([]site.ID, len(statistics.Items))
		for index, item := range statistics.Items {
			items[index] = DashboardSite{
				ID:       item.ID,
				Domain:   item.Domain,
				IsPublic: item.IsPublic,
			}
			siteIDs[index] = item.ID
		}
		result.Sites = &DashboardSites{
			Total:   statistics.Total,
			Public:  statistics.Public,
			Private: statistics.Private,
			Items:   items,
		}
	}

	if canReadResources {
		repository, ok := m.resourceRepo.(resource.StatisticsRepository)
		if !ok {
			return Dashboard{}, fmt.Errorf("resource statistics repository is unavailable")
		}
		statistics, err := repository.Statistics(ctx, resource.StatisticsQuery{
			Scope:   editScope,
			SiteIDs: siteIDs,
		})
		if err != nil {
			return Dashboard{}, fmt.Errorf("load dashboard resource statistics: %w", err)
		}
		result.Resources = &DashboardResources{Total: statistics.Total}
		if result.Sites != nil {
			for index := range result.Sites.Items {
				count := statistics.BySite[result.Sites.Items[index].ID]
				result.Sites.Items[index].ResourceCount = &count
			}
		}
	}

	if canReadUsers {
		repository, ok := m.userRepo.(user.StatisticsRepository)
		if !ok {
			return Dashboard{}, fmt.Errorf("user statistics repository is unavailable")
		}
		statistics, err := repository.Statistics(ctx)
		if err != nil {
			return Dashboard{}, fmt.Errorf("load dashboard user statistics: %w", err)
		}
		result.Users = &DashboardUsers{
			Total:   statistics.Total,
			Active:  statistics.Active,
			Blocked: statistics.Blocked,
		}
	}

	if canReadGroups {
		repository, ok := m.groupRepo.(group.StatisticsRepository)
		if !ok {
			return Dashboard{}, fmt.Errorf("group statistics repository is unavailable")
		}
		total, err := repository.Count(ctx)
		if err != nil {
			return Dashboard{}, fmt.Errorf("load dashboard group statistics: %w", err)
		}
		result.Groups = &DashboardGroups{Total: total}
	}

	return result, nil
}
