package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
)

// DeviceLister abstracts operator device enumeration for the dashboard.
// It is metadata-only by design: dashboard output never uses credentials,
// so listing must not load or enumerate them (see auth.Service.ListMetadata).
type DeviceLister interface {
	ListMetadata(ctx context.Context) ([]auth.Device, error)
}

// DashboardDeviceHandlers serves operator device connectivity + Sync Now.
// Dashboard session auth only; device credentials never valid here.
type DashboardDeviceHandlers struct {
	Svc          *devicecontrol.Service
	Auth         DeviceLister
	Stores       StoreDirectory
	OnlineWindow time.Duration
}

// StoreDirectory supplies authoritative Store context for display:
// per-device bindings plus the read-only Store registry. Nil disables
// Store columns; unbound devices show no Store rather than a fabricated
// one.
type StoreDirectory interface {
	BindingsWithStores(ctx context.Context) ([]postgres.DeviceStoreInfo, error)
	StoreSummaries(ctx context.Context) ([]store.Summary, error)
}

type deviceRow struct {
	DeviceID       string        `json:"device_id"`
	Name           string        `json:"name,omitempty"`
	StoreID        string        `json:"store_id,omitempty"`
	StoreName      string        `json:"store_name,omitempty"`
	Lifecycle      string        `json:"lifecycle"`
	Connectivity   string        `json:"connectivity"`
	LastSeenAt     *time.Time    `json:"last_seen_at,omitempty"`
	ActiveCommand  *commandWire  `json:"active_command,omitempty"`
	RecentCommands []commandWire `json:"recent_commands,omitempty"`
}

// Devices lists all devices with derived connectivity + latest command.
func (h *DashboardDeviceHandlers) Devices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	devices, err := h.Auth.ListMetadata(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	// Fixed batched reads regardless of device count: metadata (above),
	// presence + active commands + bounded history (below).
	overviews, err := h.Svc.Overview(r.Context(), ids)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	now := time.Now().UTC()
	// One bounded Store-context read for all devices; unbound devices
	// keep empty Store fields (truthfully unbound, never fabricated).
	stores := map[string]postgres.DeviceStoreInfo{}
	if h.Stores != nil {
		if infos, err := h.Stores.BindingsWithStores(r.Context()); err != nil {
			WriteError(w, r, err)
			return
		} else {
			for _, info := range infos {
				stores[info.DeviceID] = info
			}
		}
	}
	rows := make([]deviceRow, 0, len(devices))
	for _, d := range devices {
		row := deviceRow{DeviceID: d.ID, Name: d.Name, Lifecycle: string(d.Status)}
		if info, ok := stores[d.ID]; ok {
			row.StoreID, row.StoreName = info.StoreID, info.DisplayName
		}
		ov := overviews[d.ID]
		if ov.Presence != nil {
			row.LastSeenAt = ov.Presence.LastSeenAt
			row.Connectivity = devicecontrol.DeriveConnectivity(ov.Presence.LastSeenAt, now, h.OnlineWindow)
		} else {
			row.Connectivity = devicecontrol.ConnectivityNeverSeen
		}
		if ov.Active != nil {
			row.ActiveCommand = toWire(*ov.Active)
		}
		for _, c := range ov.Recent {
			row.RecentCommands = append(row.RecentCommands, *toWire(c))
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows})
}

// CreateSyncRequest queues sync_now.v1; Idempotency-Key required.
func (h *DashboardDeviceHandlers) CreateSyncRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	deviceID := deviceIDFromSyncPath(r.URL.Path)
	if deviceID == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if err := devicecontrol.ValidateIdempotencyKey(key); err != nil {
		WriteError(w, r, err)
		return
	}
	cmd, created, err := h.Svc.CreateSyncRequest(r.Context(), deviceID, key)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"command": toWire(cmd), "created": created})
}

func deviceIDFromSyncPath(path string) string {
	// /api/v1/dashboard/devices/{device_id}/sync-requests
	const prefix = "/api/v1/dashboard/devices/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if !strings.HasSuffix(rest, "/sync-requests") {
		return ""
	}
	id := strings.TrimSuffix(rest, "/sync-requests")
	id = strings.Trim(id, "/")
	if len(id) == 0 || len(id) > 64 {
		return ""
	}
	return id
}

var _ = json.Marshal
