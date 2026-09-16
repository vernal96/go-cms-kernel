package medialock

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
)

const namespace int64 = 0x4d45444900000000

func Lock(
	ctx context.Context,
	transaction pgx.Tx,
	ids ...media.ID,
) error {
	unique := make(map[media.ID]struct{}, len(ids))
	ordered := make([]media.ID, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := unique[id]; exists {
			continue
		}
		unique[id] = struct{}{}
		ordered = append(ordered, id)
	}

	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left] < ordered[right]
	})

	for _, id := range ordered {
		key := namespace ^ int64(id)
		if _, err := transaction.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock($1);",
			key,
		); err != nil {
			return fmt.Errorf(
				"lock media %d: %w",
				id,
				err,
			)
		}
	}
	return nil
}
