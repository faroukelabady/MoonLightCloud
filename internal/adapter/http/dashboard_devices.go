package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

// DeviceLister abstracts operator device enumeration for the dashboard.
type DeviceLister interface {
	List(ctx context.Context) ([]auth.Device, map[string][]auth.Credential, error)
}

// DashboardDeviceHandlers serves operator device connectivity + Sync Now.
// Dashboard session auth only; device credentials never valid here.
type DashboardDeviceHandlers struct {
	Svc          *devicecontrol.Service
	Auth         DeviceLister
	OnlineWindow time.Duration
}

type deviceRow struct {
	DeviceID       string        `json:"device_id"`
	Name           string        `json:"name,omitempty"`
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
	devices, _, err := h.Auth.List(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	now := time.Now().UTC()
	rows := make([]deviceRow, 0, len(devices))
	for _, d := range devices {
		row := deviceRow{DeviceID: d.ID, Name: d.Name, Lifecycle: string(d.Status)}
		presence, ok, perr := h.Svc.Presence(r.Context(), d.ID)
		if perr != nil {
			WriteError(w, r, perr)
			return
		}
		if ok {
			row.LastSeenAt = presence.LastSeenAt
			row.Connectivity = devicecontrol.DeriveConnectivity(presence.LastSeenAt, now, h.OnlineWindow)
		} else {
			row.Connectivity = devicecontrol.ConnectivityNeverSeen
		}
		if active, ok, aerr := h.Svc.Active(r.Context(), d.ID); aerr != nil {
			WriteError(w, r, aerr)
			return
		} else if ok {
			row.ActiveCommand = toWire(active)
		}
		if recent, rerr := h.Svc.Recent(r.Context(), d.ID, 5); rerr != nil {
			WriteError(w, r, rerr)
			return
		} else {
			for _, c := range recent {
				row.RecentCommands = append(row.RecentCommands, *toWire(c))
			}
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
		WriteError(w, r, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION"))
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
