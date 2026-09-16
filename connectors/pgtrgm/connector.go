// Package pgtrgm provides transaction-scoped PostgreSQL trigram search tools.
// The application owns the underlying PostgreSQL connection pool.
package pgtrgm

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/connectors/postgres"
)

type Connector struct{ postgres *postgres.Connector }

func New(connector *postgres.Connector) (*Connector, error) {
	if connector == nil || connector.Pool() == nil {
		return nil, errors.New("pgtrgm requires a PostgreSQL connection")
	}
	return &Connector{postgres: connector}, nil
}

// Read executes related searches in one snapshot. Settings never escape the
// transaction, even when fn fails or its context is cancelled.
func (c *Connector) Read(ctx context.Context, threshold float64, fn func(pgx.Tx) error) error {
	if ctx == nil || fn == nil || math.IsNaN(threshold) || threshold < 0 || threshold > 1 {
		return errors.New("invalid pgtrgm read")
	}
	tx, err := c.postgres.Pool().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SELECT set_config('pg_trgm.word_similarity_threshold', $1, true)`, strconv.FormatFloat(threshold, 'f', -1, 64)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ContainsPattern treats user input literally, including SQL LIKE metacharacters.
func ContainsPattern(text string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text) + "%"
}
