package http

import (
	"net/http"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// StoreHandlers serves the read-only operator Store list (dashboard
// session auth only). No financial, inventory, or cross-Store views.
type StoreHandlers struct {
	Stores StoreDirectory
}

type storeRow struct {
	StoreID     string    `json:"store_id"`
	DisplayName string    `json:"display_name"`
	Timezone    string    `json:"timezone"`
	Status      string    `json:"status"`
	DeviceCount int64     `json:"device_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Stores lists registered Stores with bound device counts in one
// aggregate read. Empty registry yields an empty list, never an error.
func (h *StoreHandlers) ListStores(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	summaries, err := h.Stores.StoreSummaries(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	principal, _ := PrincipalOf(r)
	rows := make([]storeRow, 0, len(summaries))
	for _, summary := range summaries {
		if !principal.CanAccessStore(summary.ID) {
			continue // ADR-0053: only Stores the human may administer
		}
		rows = append(rows, storeRow{
			StoreID: summary.ID, DisplayName: summary.DisplayName,
			Timezone: summary.Timezone, Status: summary.Status,
			DeviceCount: summary.DeviceCount,
			CreatedAt:   summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"stores": rows})
}
