package store

import "context"

// CommitPoolLeases confirms a bounded set in one transaction. A failed item
// rolls back the whole set; repeating the same used leases is idempotent.
func (s *Store) CommitPoolLeases(ctx context.Context, clientID string, ids []string) ([]PoolLease, error) {
	if clientID == "" || len(ids) < 1 || len(ids) > 50 {
		return nil, ErrPoolInput
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, ErrPoolInput
		}
		seen[id] = true
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	leases := make([]PoolLease, 0, len(ids))
	for _, id := range ids {
		lease, err := s.actPoolLeaseTx(ctx, tx, id, clientID, "commit", 0)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return leases, nil
}
