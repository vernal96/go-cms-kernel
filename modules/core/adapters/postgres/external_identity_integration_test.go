package postgres

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestExternalIdentityRegistrationLinkingAndRevocation(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	repository := database.Users().(user.ExternalIdentityRepository)
	suffix := fmt.Sprint(time.Now().UnixNano())
	candidate := user.Record{User: user.User{
		Login: "external-" + suffix, Email: "external-" + suffix + "@example.test", Name: "External",
		ColorScheme: user.ColorSchemeSystem, AccentColor: user.AccentColorBlue,
	}}
	input := user.ExternalInput{
		Provider: "audit", Namespace: "audit-client", Subject: suffix, AllowRegistration: true,
	}
	created, err := repository.AuthenticateExternal(ctx, input, candidate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connector.Pool().Exec(ctx, "DELETE FROM core.users WHERE id=$1", created.ID) })
	if created.ID <= 0 || created.PasswordHash != "" {
		t.Fatal("social-only credential was manufactured")
	}
	var groupCount int
	if err := connector.Pool().QueryRow(ctx,
		"SELECT count(*) FROM core.user_groups WHERE user_id=$1", created.ID).Scan(&groupCount); err != nil {
		t.Fatal(err)
	}
	if groupCount != 0 {
		t.Fatal("public registration assigned groups")
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			again, err := repository.AuthenticateExternal(ctx, input, candidate)
			if err != nil || again.ID != created.ID {
				t.Errorf("concurrent login: %v %+v", err, again)
			}
		}()
	}
	wait.Wait()
	// An unbound identity with a colliding email may never claim the account.
	input.Subject += "-other"
	candidate.Login += "-other"
	_, err = repository.AuthenticateExternal(ctx, input, candidate)
	if !errors.Is(err, user.ErrEmailExists) {
		t.Fatalf("email collision: %v", err)
	}
	input.LinkUserID = &created.ID
	input.LinkSessionVersion = created.SessionVersion - 1
	_, err = repository.AuthenticateExternal(ctx, input, candidate)
	if !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("stale link: %v", err)
	}
	input.LinkSessionVersion = created.SessionVersion
	linked, err := repository.AuthenticateExternal(ctx, input, candidate)
	if err != nil || linked.ID != created.ID {
		t.Fatalf("authenticated link: %v", err)
	}
	if _, err := database.Users().Block(ctx, nil, created.ID); err != nil {
		t.Fatal(err)
	}
	input.LinkUserID = nil
	_, err = repository.AuthenticateExternal(ctx, input, candidate)
	if !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("blocked social login: %v", err)
	}
}

func TestExternalIdentityAllowsNoEmailAndHonorsClosedRegistration(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	repository := database.Users().(user.ExternalIdentityRepository)
	suffix := fmt.Sprint(time.Now().UnixNano())
	input := user.ExternalInput{Provider: "audit", Namespace: "audit-client", Subject: suffix}
	candidate := user.Record{User: user.User{
		Login: "external-" + suffix, Name: "No email",
		ColorScheme: user.ColorSchemeSystem, AccentColor: user.AccentColorBlue,
	}}
	_, err := repository.AuthenticateExternal(ctx, input, candidate)
	if !errors.Is(err, user.ErrRegistrationDisabled) {
		t.Fatalf("closed registration: %v", err)
	}
	input.AllowRegistration = true
	created, err := repository.AuthenticateExternal(ctx, input, candidate)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Pool().Exec(ctx, "DELETE FROM core.users WHERE id=$1", created.ID)
	if created.Email != "" || created.PasswordHash != "" {
		t.Fatal("missing credentials were manufactured")
	}
	loaded, err := database.Users().ByID(ctx, created.ID)
	if err != nil || loaded.Email != "" {
		t.Fatalf("load optional email: %v", err)
	}
	input.AllowRegistration = false
	again, err := repository.AuthenticateExternal(ctx, input, candidate)
	if err != nil || again.ID != created.ID {
		t.Fatal("closed registration blocked existing account", err)
	}
}

func TestExternalIdentityConcurrentFirstRegistration(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	repository := database.Users().(user.ExternalIdentityRepository)
	suffix := fmt.Sprint(time.Now().UnixNano())
	input := user.ExternalInput{
		Provider: "audit", Namespace: "concurrent-client", Subject: suffix, AllowRegistration: true,
	}
	candidate := user.Record{User: user.User{
		Login: "external-" + suffix, Name: "Concurrent",
		ColorScheme: user.ColorSchemeSystem, AccentColor: user.AccentColorBlue,
	}}
	results := make(chan user.ID, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := repository.AuthenticateExternal(ctx, input, candidate)
			if err != nil {
				t.Error(err)
				return
			}
			results <- record.ID
		}()
	}
	wait.Wait()
	close(results)
	var expected user.ID
	count := 0
	for id := range results {
		if expected == 0 {
			expected = id
			defer connector.Pool().Exec(ctx, "DELETE FROM core.users WHERE id=$1", id)
		}
		if id != expected {
			t.Error("concurrent registration created another user")
		}
		count++
	}
	if count != 8 {
		t.Fatal("concurrent registration failed", count)
	}
}
