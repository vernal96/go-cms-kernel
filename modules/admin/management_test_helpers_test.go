package admin

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/eventbus"
	coremanagement "github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type extensionTestDatabaseResolver struct{}

func (extensionTestDatabaseResolver) MainModuleDatabase(kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}

func (extensionTestDatabaseResolver) ModuleDatabase(kernel.ConnectionCode, kernel.ModuleCode) (kernel.ModuleDatabase, bool) {
	return nil, false
}

type extensionTestBus struct{}

func (extensionTestBus) Publish(context.Context, eventbus.Message) error { return nil }
func (extensionTestBus) Consume(context.Context, eventbus.Subscription, eventbus.Handler) error {
	return nil
}

type extensionTestSites struct{ runtime *site.Runtime }

func (s extensionTestSites) RuntimeByID(id site.ID) (*site.Runtime, bool) {
	return s.runtime, s.runtime != nil && s.runtime.Site().ID == id
}

func (extensionTestSites) Create(context.Context, security.Actor, site.CreateInput) (*site.Runtime, error) {
	return nil, errors.New("not implemented")
}

func (extensionTestSites) Update(context.Context, security.Actor, site.UpdateInput) (*site.Runtime, error) {
	return nil, errors.New("not implemented")
}

func (extensionTestSites) Delete(context.Context, security.Actor, site.ID) error {
	return errors.New("not implemented")
}

type managementAuthorizer struct {
	denied map[permission.Code]error
}

func (a managementAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	return a.denied[code]
}

type scopedPolicy struct {
	scope  coremanagement.SiteAccessScope
	scopes map[coremanagement.SiteAccessAction]coremanagement.SiteAccessScope
	checks map[coremanagement.SiteAccessAction]error
}

func (p scopedPolicy) Scope(_ context.Context, _ security.Actor, action coremanagement.SiteAccessAction) (coremanagement.SiteAccessScope, error) {
	if scope, exists := p.scopes[action]; exists {
		return scope, nil
	}
	return p.scope, nil
}

func (p scopedPolicy) Check(_ context.Context, _ security.Actor, _ site.ID, action coremanagement.SiteAccessAction) error {
	return p.checks[action]
}
