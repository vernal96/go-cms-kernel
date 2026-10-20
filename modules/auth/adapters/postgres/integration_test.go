package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/migrations"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

func openFlowRepository(t *testing.T) (*Repository, context.Context) {
	t.Helper()
	if os.Getenv("CMS_TEST_POSTGRES_HOST") == "" {
		t.Skip("set CMS_TEST_POSTGRES_HOST to run the PostgreSQL integration test")
	}
	port := 5432
	if value := os.Getenv("CMS_TEST_POSTGRES_PORT"); value != "" {
		var err error
		port, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	sslMode := os.Getenv("CMS_TEST_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{
		Code: "auth-integration", Host: os.Getenv("CMS_TEST_POSTGRES_HOST"), Port: port,
		Database: os.Getenv("CMS_TEST_POSTGRES_DB"), User: os.Getenv("CMS_TEST_POSTGRES_USER"),
		Password: os.Getenv("CMS_TEST_POSTGRES_PASSWORD"), SSLMode: sslMode,
		MaxConns: 4, ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	plan := migrations.Plan{
		Connection: string(connector.Code()), Target: connector, Source: database.MigrationSources()[0],
	}
	if err := migrations.NewManager().Up(ctx, plan); err != nil {
		t.Fatal(err)
	}
	return database.repository, ctx
}

func TestFlowRepositoryAtomicallyConsumesOnlyMatchingBrowserSiteAndProvider(t *testing.T) {
	repository, ctx := openFlowRepository(t)
	suffix := fmt.Sprintf("%064d", time.Now().UnixNano())
	flow := auth.Flow{
		StateHash: suffix, BrowserHash: suffix, SiteID: time.Now().UnixNano(),
		Provider: "demo", Namespace: "client-a", RedirectURI: "https://site.test/callback",
		Nonce: "nonce", CodeVerifier: "verifier", ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := repository.Create(ctx, flow); err != nil {
		t.Fatal(err)
	}
	defer repository.connector.Pool().Exec(ctx, "DELETE FROM auth.flows WHERE site_id=$1", flow.SiteID)
	for _, attempt := range []struct {
		browser     string
		site        int64
		provider    string
		redirectURI string
	}{
		{"wrong", flow.SiteID, flow.Provider, flow.RedirectURI},
		{flow.BrowserHash, flow.SiteID + 1, flow.Provider, flow.RedirectURI},
		{flow.BrowserHash, flow.SiteID, "other", flow.RedirectURI},
		{flow.BrowserHash, flow.SiteID, flow.Provider, "https://site.test:8443/callback"},
	} {
		_, err := repository.Consume(ctx, flow.StateHash, attempt.browser,
			attempt.site, attempt.provider, attempt.redirectURI, time.Now())
		if !errors.Is(err, auth.ErrState) {
			t.Fatalf("binding mismatch: %v", err)
		}
	}
	var wait sync.WaitGroup
	var successes atomic.Int32
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			consumed, err := repository.Consume(ctx, flow.StateHash, flow.BrowserHash,
				flow.SiteID, flow.Provider, flow.RedirectURI, time.Now())
			if err == nil {
				successes.Add(1)
				if consumed.CodeVerifier != flow.CodeVerifier {
					t.Error("flow lost verifier")
				}
			} else if !errors.Is(err, auth.ErrState) {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatal("flow consumed", successes.Load(), "times")
	}
}

func TestFlowRepositoryExpiresAndPrunesOnlyItsSite(t *testing.T) {
	repository, ctx := openFlowRepository(t)
	state := fmt.Sprintf("%064d", time.Now().UnixNano())
	flow := auth.Flow{
		StateHash: state, BrowserHash: state, SiteID: time.Now().UnixNano(),
		Provider: "demo", Namespace: "client", RedirectURI: "https://site.test/callback",
		Nonce: "nonce", CodeVerifier: "verifier", ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := repository.Create(ctx, flow); err != nil {
		t.Fatal(err)
	}
	defer repository.connector.Pool().Exec(ctx, "DELETE FROM auth.flows WHERE site_id=$1", flow.SiteID)
	_, err := repository.Consume(ctx, flow.StateHash, flow.BrowserHash,
		flow.SiteID, flow.Provider, flow.RedirectURI, time.Now())
	if !errors.Is(err, auth.ErrState) {
		t.Fatal("expired flow accepted", err)
	}
	if err := repository.Prune(ctx, flow.SiteID+1, time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repository.connector.Pool().QueryRow(ctx,
		"SELECT count(*) FROM auth.flows WHERE state_hash=$1", state).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("another site pruned this flow")
	}
	if err := repository.Prune(ctx, flow.SiteID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := repository.connector.Pool().QueryRow(ctx,
		"SELECT count(*) FROM auth.flows WHERE state_hash=$1", state).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("expired flow was retained")
	}
}
