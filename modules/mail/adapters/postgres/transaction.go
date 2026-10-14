package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func rollback(tx pgx.Tx, resultErr *error) {
	err := tx.Rollback(context.Background())
	if *resultErr != nil && err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		*resultErr = errors.Join(*resultErr, err)
	}
}
