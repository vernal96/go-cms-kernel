package management

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type GroupSiteAccessPolicy struct {
	repository group.Repository
	access     access.Service
}

func NewGroupSiteAccessPolicy(
	repository group.Repository,
	accessService access.Service,
) (*GroupSiteAccessPolicy, error) {
	if repository == nil {
		return nil, errors.New("site access group repository is nil")
	}
	if accessService == nil {
		return nil, errors.New("site access service is nil")
	}
	return &GroupSiteAccessPolicy{repository: repository, access: accessService}, nil
}

func (p *GroupSiteAccessPolicy) Scope(
	ctx context.Context,
	actor security.Actor,
	action SiteAccessAction,
) (SiteAccessScope, error) {
	if err := validateSiteAccessAction(action); err != nil {
		return SiteAccessScope{}, err
	}
	privileged, err := p.access.IsPrivileged(ctx, actor)
	if err != nil {
		return SiteAccessScope{}, err
	}
	if privileged {
		return SiteAccessScope{All: true}, nil
	}
	userID, exists := actor.UserID()
	if !exists {
		return SiteAccessScope{}, security.ErrUnauthenticated
	}
	ids, err := p.repository.EffectiveSiteIDs(ctx, userID, action)
	if err != nil {
		return SiteAccessScope{}, fmt.Errorf("resolve site access scope: %w", err)
	}
	return SiteAccessScope{SiteIDs: ids}, nil
}

func (p *GroupSiteAccessPolicy) Check(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
	action SiteAccessAction,
) error {
	if siteID <= 0 {
		return errors.New("invalid site id")
	}
	if err := validateSiteAccessAction(action); err != nil {
		return err
	}
	privileged, err := p.access.IsPrivileged(ctx, actor)
	if err != nil {
		return err
	}
	if privileged {
		return nil
	}
	userID, exists := actor.UserID()
	if !exists {
		return security.ErrUnauthenticated
	}
	allowed, err := p.repository.UserHasSiteAccess(ctx, userID, siteID, action)
	if err != nil {
		return fmt.Errorf("check site access: %w", err)
	}
	if !allowed {
		return security.ErrForbidden
	}
	return nil
}

func validateSiteAccessAction(action SiteAccessAction) error {
	switch action {
	case SiteAccessView, SiteAccessEdit, SiteAccessDelete:
		return nil
	default:
		return errors.New("invalid site access action")
	}
}

var _ SiteAccessPolicy = (*GroupSiteAccessPolicy)(nil)
