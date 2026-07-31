package store

import (
	"context"
	"fmt"
)

// SiteUpdate carries optional site field updates; nil = leave unchanged.
// Latitude and Longitude must be set together (the DB CHECK enforces
// both-or-neither, and the CLI validates before calling).
type SiteUpdate struct {
	Latitude    *float64
	Longitude   *float64
	DisplayName *string
	Location    *string
}

// UpdateSite updates an existing site by name. Unknown names are an error —
// admin commands never auto-create sites (a typo'd --site must fail loudly),
// unlike token creation's EnsureSite.
func (s *Store) UpdateSite(ctx context.Context, name string, up SiteUpdate) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sites
		   SET latitude     = COALESCE($2, latitude),
		       longitude    = COALESCE($3, longitude),
		       display_name = COALESCE($4, display_name),
		       location     = COALESCE($5, location)
		 WHERE name = $1`,
		name, up.Latitude, up.Longitude, up.DisplayName, up.Location)
	if err != nil {
		return fmt.Errorf("update site %q: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("site %q does not exist", name)
	}
	return nil
}
