package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
)

// Catalog diagnostics delegate only to the shared processing-state adapter.
func (d CatalogProjection) ProcessingStats(ctx context.Context, processor string) (projection.Stats, error) {
	return NewProjectionState(d.pool, d.timeout).ProcessingStats(ctx, processor)
}
func (d CatalogProjection) ResetProcessing(ctx context.Context, processor, eventID string) error {
	return NewProjectionState(d.pool, d.timeout).ResetProcessing(ctx, processor, eventID)
}
