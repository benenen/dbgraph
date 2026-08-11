package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/benenen/dbgraph/internal/id"
)

func (s *Store) ReserveIDRange(
	ctx context.Context,
	node uint16,
	observedTimestamp int64,
	count int,
) (id.Reservation, error) {
	var reservation id.Reservation
	err := s.write(ctx, func(tx *sql.Tx) error {
		var highWater sql.NullInt64
		err := tx.QueryRowContext(
			ctx,
			"SELECT high_water_id FROM id_generator_states WHERE node_id = ?",
			node,
		).Scan(&highWater)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read persistent ID high-water: %w", err)
		}

		reservation, err = id.ReserveRange(node, observedTimestamp, highWater.Int64, count)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO id_generator_states(node_id, high_water_id, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(node_id) DO UPDATE SET
    high_water_id = excluded.high_water_id,
    updated_at = excluded.updated_at
`, node, reservation.LastID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("persist ID high-water: %w", err)
		}
		return nil
	})
	return reservation, err
}
