package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

// DeviceControlHandlers serves authenticated Retail control endpoints.
// Retail initiates every connection; Cloud never dials Retail.
type DeviceControlHandlers struct {
	Svc *devicecontrol.Service
	Log interface {
		Info(string, ...any)
	}
}

type pollResponse struct {
	ServerTime time.Time    `json:"server_time"`
	Command    *commandWire `json:"command,omitempty"`
}

type commandWire struct {
	ID              string     `json:"id"`
	Type            string     `json:"type"`
	Version         int        `json:"version"`
	LeaseGeneration int64      `json:"lease_generation"`
	RequestedAt     time.Time  `json:"requested_at"`
	Status          string     `json:"status"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	RunningAt       *time.Time `json:"running_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ResultCode      *string    `json:"result_code,omitempty"`
}

func toWire(c devicecontrol.Command) *commandWire {
	return &commandWire{
		ID: c.ID, Type: c.Type, Version: c.Version,
		LeaseGeneration: c.LeaseGeneration, RequestedAt: c.RequestedAt,
		Status: c.Status, AcceptedAt: c.AcceptedAt, RunningAt: c.RunningAt,
		FinishedAt: c.FinishedAt, ResultCode: c.ResultCode,
	}
}

// Poll authenticates the device (middleware), refreshes last-seen, claims
// at most one due command. Body is empty; response is minimal.
func (h *DeviceControlHandlers) Poll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	cmd, claimed, err := h.Svc.Poll(r.Context(), dev.ID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	resp := pollResponse{ServerTime: time.Now().UTC()}
	if claimed {
		if cmd.Type != devicecontrol.CommandSyncNow || cmd.Version != devicecontrol.CommandVersionV1 {
			WriteError(w, r, apperr.New(apperr.Internal, "internal error"))
			return
		}
		resp.Command = toWire(cmd)
	}
	writeJSON(w, http.StatusOK, resp)
}

// Accepted records durable Retail receipt; idempotent.
func (h *DeviceControlHandlers) Accepted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	id := commandIDFromPath(r.URL.Path)
	if id == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND"))
		return
	}
	cmd, err := h.Svc.Accept(r.Context(), dev.ID, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": toWire(cmd)})
}

// Status accepts running/completed/failed reports; first terminal wins.
func (h *DeviceControlHandlers) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	id := commandIDFromPath(r.URL.Path)
	if id == "" {
		WriteError(w, r, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND"))
		return
	}
	var body struct {
		Status     string `json:"status"`
		ResultCode string `json:"result_code"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(&body); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION"))
		return
	}
	var cmd devicecontrol.Command
	var err error
	switch body.Status {
	case devicecontrol.StatusRunning:
		cmd, err = h.Svc.ReportRunning(r.Context(), dev.ID, id)
	case devicecontrol.StatusCompleted, devicecontrol.StatusFailed:
		if body.ResultCode == "" {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION"))
			return
		}
		cmd, err = h.Svc.ReportTerminal(r.Context(), dev.ID, id, body.Status, body.ResultCode)
	default:
		WriteError(w, r, apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_TRANSITION"))
		return
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"command": toWire(cmd)})
}

func commandIDFromPath(path string) string {
	// /api/v1/device-control/commands/{id}/(accepted|status)
	const prefix = "/api/v1/device-control/commands/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.TrimSuffix(rest, "/")
	if i := strings.LastIndex(rest, "/"); i >= 0 {
		rest = rest[:i]
	} else {
		return ""
	}
	if len(rest) == 0 || len(rest) > 64 {
		return ""
	}
	return rest
}
