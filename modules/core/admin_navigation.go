package core

import (
	"github.com/vernal96/go-cms-kernel/adminui"
	"github.com/vernal96/go-cms-kernel/permission"
)

func (Module) AdminNavigation() []adminui.NavigationItem {
	return []adminui.NavigationItem{
		{
			Code:       "sites",
			Label:      "Сайты",
			Route:      "core.sites",
			Icon:       "sites",
			Order:      100,
			Permission: permission.MustCode("core", "site", permission.Read),
			Scope:      adminui.NavigationGlobal,
		},
		{
			Code:       "files",
			Label:      "Файловая система",
			Route:      "core.files",
			Icon:       "files",
			Order:      200,
			Permission: permission.MustCode("core", "file", permission.Read),
			Scope:      adminui.NavigationGlobal,
		},
		{
			Code: "tools", Label: "Инструменты", Icon: "tools", Order: 400, Scope: adminui.NavigationGlobal,
			Children: []adminui.NavigationItem{{
				Code: "administration", Label: "Администрирование", Route: "core.administration",
				Icon: "administration", Order: 900, Scope: adminui.NavigationGlobal,
				Visibility: adminui.NavigationAdministrator,
			}},
		},
		{
			Code:  "identity",
			Label: "Пользователи",
			Icon:  "users",
			Order: 300,
			Scope: adminui.NavigationGlobal,
			Children: []adminui.NavigationItem{
				{
					Code:       "users",
					Icon:       "users",
					Label:      "Пользователи",
					Route:      "core.users",
					Order:      100,
					Permission: permission.MustCode("core", "user", permission.Read),
					Scope:      adminui.NavigationGlobal,
				},
				{
					Code:       "groups",
					Icon:       "groups",
					Label:      "Группы",
					Route:      "core.groups",
					Order:      200,
					Permission: permission.MustCode("core", "group", permission.Read),
					Scope:      adminui.NavigationGlobal,
				},
			},
		},
	}
}

var _ adminui.NavigationProvider = Module{}
