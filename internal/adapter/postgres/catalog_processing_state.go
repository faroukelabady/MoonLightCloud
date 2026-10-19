package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
)

// Catalog still uses Devices during the bounded adapter migration. Only these
// explicit diagnostics methods delegate; no business methods are embedded.
func (d Devices) ProcessingStats(ctx context.Context, processor string) (projection.Stats, error) {
	return NewProjectionState(d.pool, d.timeout).ProcessingStats(ctx, processor)
}
func (d Devices) ResetProcessing(ctx context.Context, processor, eventID string) error {
	return NewProjectionState(d.pool, d.timeout).ResetProcessing(ctx, processor, eventID)
}
