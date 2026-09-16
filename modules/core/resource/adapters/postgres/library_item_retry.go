package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
)

const libraryItemTransactionAttempts = 3

func retryLibraryItemTransaction(
	ctx context.Context,
	operation func() (resource.LibraryItem, error),
) (resource.LibraryItem, error) {
	var lastErr error
	for attempt := 0; attempt < libraryItemTransactionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return resource.LibraryItem{}, err
		}
		item, err := operation()
		if err == nil {
			return item, nil
		}
		lastErr = err
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != pgerrcode.SerializationFailure {
			return resource.LibraryItem{}, err
		}
	}
	return resource.LibraryItem{}, lastErr
}
