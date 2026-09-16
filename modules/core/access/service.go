package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type ApplicationService struct {
	repository Repository
	catalog    *permission.Catalog
}

func NewService(
	repository Repository,
	catalog *permission.Catalog,
) (*ApplicationService, error) {
	if repository == nil {
		return nil, errors.New("access repository is nil")
	}
	if catalog == nil {
		return nil, errors.New("permission catalog is nil")
	}

	return &ApplicationService{
		repository: repository,
		catalog:    catalog,
	}, nil
}

func (s *ApplicationService) Check(
	ctx context.Context,
	actor security.Actor,
	code permission.Code,
) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := s.catalog.Require(code); err != nil {
		return err
	}
	if actor.IsSystem() {
		return nil
	}

	if actor.IsGuest() {
		return s.checkGuest(ctx, code)
	}

	userID, exists := actor.UserID()
	if !exists {
		return security.ErrUnauthenticated
	}

	subject, err := s.repository.Subject(ctx, userID)
	if err != nil {
		return fmt.Errorf("load authorization subject: %w", err)
	}
	if !subject.Exists || !subject.Active {
		return security.ErrUnauthenticated
	}
	if subject.IsSuper {
		return nil
	}
	if !subject.HasGroups {
		return s.checkGuest(ctx, code)
	}

	allowed, err := s.repository.GroupAllowed(ctx, userID, code)
	if err != nil {
		return fmt.Errorf("check group permission %q: %w", code, err)
	}
	if !allowed {
		return security.ErrForbidden
	}
	return nil
}

func (s *ApplicationService) Codes() []permission.Code {
	return s.catalog.Codes()
}

func (s *ApplicationService) IsPrivileged(
	ctx context.Context,
	actor security.Actor,
) (bool, error) {
	if err := validateContext(ctx); err != nil {
		return false, err
	}
	if actor.IsSystem() {
		return true, nil
	}
	userID, exists := actor.UserID()
	if !exists {
		return false, nil
	}
	subject, err := s.repository.Subject(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load privileged subject: %w", err)
	}
	if !subject.Exists || !subject.Active {
		return false, security.ErrUnauthenticated
	}
	return subject.IsSuper, nil
}

func (s *ApplicationService) IsAdministrator(ctx context.Context, actor security.Actor) (bool, error) {
	if err := validateContext(ctx); err != nil {
		return false, err
	}
	if actor.IsSystem() {
		return true, nil
	}
	userID, exists := actor.UserID()
	if !exists {
		return false, security.ErrUnauthenticated
	}
	subject, err := s.repository.Subject(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load administrator subject: %w", err)
	}
	if !subject.Exists || !subject.Active {
		return false, security.ErrUnauthenticated
	}
	repository, ok := s.repository.(AdministratorRepository)
	if !ok {
		return false, errors.New("administrator membership repository is unavailable")
	}
	allowed, err := repository.IsAdministrator(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("check administrator membership: %w", err)
	}
	return allowed, nil
}

func (s *ApplicationService) IsGuestSubject(
	ctx context.Context,
	actor security.Actor,
) (bool, error) {
	if err := validateContext(ctx); err != nil {
		return false, err
	}
	if actor.IsSystem() {
		return false, nil
	}
	if actor.IsGuest() {
		return true, nil
	}
	userID, exists := actor.UserID()
	if !exists {
		return false, security.ErrUnauthenticated
	}
	subject, err := s.repository.Subject(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load guest subject: %w", err)
	}
	if !subject.Exists || !subject.Active {
		return false, security.ErrUnauthenticated
	}
	if subject.IsSuper {
		return false, nil
	}
	return !subject.HasGroups, nil
}

func (s *ApplicationService) GuestPermissions(
	ctx context.Context,
	actor security.Actor,
) ([]Grant, error) {
	if err := s.requirePrivileged(ctx, actor); err != nil {
		return nil, err
	}
	grants, err := s.repository.GuestPermissions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list guest permissions: %w", err)
	}
	return cloneGrants(grants), nil
}

func (s *ApplicationService) GrantGuest(
	ctx context.Context,
	actor security.Actor,
	code permission.Code,
) (Grant, error) {
	if err := s.requirePrivileged(ctx, actor); err != nil {
		return Grant{}, err
	}
	if err := s.catalog.Require(code); err != nil {
		return Grant{}, err
	}
	grant, err := s.repository.GrantGuest(
		ctx,
		actor.AuditUserID(),
		code,
	)
	if err != nil {
		return Grant{}, fmt.Errorf("grant guest permission %q: %w", code, err)
	}
	return cloneGrant(grant), nil
}

func (s *ApplicationService) RevokeGuest(
	ctx context.Context,
	actor security.Actor,
	code permission.Code,
) error {
	if err := s.requirePrivileged(ctx, actor); err != nil {
		return err
	}
	if err := s.catalog.Require(code); err != nil {
		return err
	}
	if err := s.repository.RevokeGuest(ctx, code); err != nil {
		return fmt.Errorf("revoke guest permission %q: %w", code, err)
	}
	return nil
}

func (s *ApplicationService) checkGuest(
	ctx context.Context,
	code permission.Code,
) error {
	allowed, err := s.repository.GuestAllowed(ctx, code)
	if err != nil {
		return fmt.Errorf("check guest permission %q: %w", code, err)
	}
	if !allowed {
		return security.ErrForbidden
	}
	return nil
}

func (s *ApplicationService) requirePrivileged(
	ctx context.Context,
	actor security.Actor,
) error {
	privileged, err := s.IsPrivileged(ctx, actor)
	if err != nil {
		return err
	}
	if !privileged {
		return ErrNotPrivileged
	}
	return nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("access context is nil")
	}
	return ctx.Err()
}

func cloneGrants(source []Grant) []Grant {
	result := make([]Grant, len(source))
	for index, grant := range source {
		result[index] = cloneGrant(grant)
	}
	return result
}

func cloneGrant(grant Grant) Grant {
	grant.CreatedBy = cloneUserID(grant.CreatedBy)
	grant.UpdatedBy = cloneUserID(grant.UpdatedBy)
	return grant
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

var _ Service = (*ApplicationService)(nil)
