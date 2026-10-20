package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/auth"
)

type Repository struct{ connector *connectorpostgres.Connector }

func (r *Repository) Create(ctx context.Context, flow auth.Flow) error {
	tx, err := r.connector.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Bound durable state under concurrent requests on all replicas.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"auth.flows:"+strconv.FormatInt(flow.SiteID, 10))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM auth.flows WHERE site_id=$1 AND expires_at<=now()`, flow.SiteID)
	if err != nil {
		return err
	}
	var pending int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM auth.flows WHERE site_id=$1`, flow.SiteID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending >= 10000 {
		return auth.ErrBusy
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth.flows
(state_hash,browser_hash,site_id,provider,namespace,redirect_uri,nonce,code_verifier,
link_user_id,link_session_version,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		flow.StateHash, flow.BrowserHash, flow.SiteID, flow.Provider, flow.Namespace,
		flow.RedirectURI, flow.Nonce, flow.CodeVerifier, flow.LinkUserID,
		flow.LinkSessionVersion, flow.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) Consume(
	ctx context.Context,
	stateHash, browserHash string,
	siteID int64,
	provider string,
	redirectURI string,
	now time.Time,
) (auth.Flow, error) {
	var flow auth.Flow
	err := r.connector.Pool().QueryRow(ctx, `DELETE FROM auth.flows
WHERE state_hash=$1 AND browser_hash=$2 AND site_id=$3 AND provider=$4
AND redirect_uri=$5 AND expires_at>$6
RETURNING state_hash,browser_hash,site_id,provider,namespace,redirect_uri,nonce,
code_verifier,link_user_id,link_session_version,expires_at`,
		stateHash, browserHash, siteID, provider, redirectURI, now,
	).Scan(&flow.StateHash, &flow.BrowserHash, &flow.SiteID, &flow.Provider,
		&flow.Namespace, &flow.RedirectURI, &flow.Nonce, &flow.CodeVerifier,
		&flow.LinkUserID, &flow.LinkSessionVersion, &flow.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Flow{}, auth.ErrState
	}
	return flow, err
}

func (r *Repository) Prune(ctx context.Context, siteID int64, now time.Time) error {
	_, err := r.connector.Pool().Exec(ctx,
		`DELETE FROM auth.flows WHERE site_id=$1 AND expires_at<=$2`, siteID, now)
	return err
}

var _ auth.FlowRepository = (*Repository)(nil)
