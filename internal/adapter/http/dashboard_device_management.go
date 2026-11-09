package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	"github.com/google/uuid"
)

type DeviceManager interface {
	CreateForStore(context.Context, string, string) (auth.Provisioned, error)
	Rotate(context.Context, string) (auth.Provisioned, error)
	RevokeDevice(context.Context, string) error
}
type StoreCreator interface {
	CreateStore(context.Context, store.RegistrationRequest) (store.Store, error)
}
type DeviceManagementAudit interface {
	Audit(context.Context, humanauth.AuditEvent)
}

func (h *DashboardDeviceHandlers) approveDeviceAction(w http.ResponseWriter, r *http.Request, code, action string) bool {
	return requireFreshStepUp(w, r, h.StepUp, h.ClientIP, code, action)
}
func (h *DashboardDeviceHandlers) managementAudit(r *http.Request, action, outcome, id, storeID string) {
	if h.Audit == nil {
		return
	}
	p, _ := PrincipalOf(r)
	h.Audit.Audit(r.Context(), humanauth.AuditEvent{ActorUserID: p.UserID, Action: action, Outcome: outcome, Details: map[string]any{"device_id": id, "store_id": storeID}})
}
func (h *DashboardDeviceHandlers) managedStore(w http.ResponseWriter, r *http.Request, deviceID string, allowUnbound bool) (string, bool) {
	if _, err := uuid.Parse(deviceID); err != nil {
		WriteError(w, r, apperr.New(apperr.NotFound, "device not found"))
		return "", false
	}
	if h.Stores == nil {
		WriteError(w, r, apperr.New(apperr.Unavailable, "device management unavailable"))
		return "", false
	}
	bindings, err := h.Stores.BindingsWithStores(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return "", false
	}
	for _, binding := range bindings {
		if binding.DeviceID == deviceID {
			return binding.StoreID, RequireStore(w, r, binding.StoreID)
		}
	}
	if principal, ok := PrincipalOf(r); ok && principal.AllStores && allowUnbound && h.Auth != nil {
		devices, err := h.Auth.ListMetadata(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return "", false
		}
		for _, device := range devices {
			if device.ID == deviceID {
				return "", true
			}
		}
	}
	WriteError(w, r, apperr.New(apperr.NotFound, "bound device not found"))
	return "", false
}
func noCredentialCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
func (h *DashboardDeviceHandlers) provisionResponse(w http.ResponseWriter, p auth.Provisioned, storeID string, status int) {
	noCredentialCache(w)
	writeJSON(w, status, map[string]any{"device_id": p.Device.ID, "credential_id": p.Credential.ID, "name": p.Device.Name, "store_id": storeID, "credential": p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret})
}

func (h *DashboardDeviceHandlers) CreateStore(w http.ResponseWriter, r *http.Request) {
	noCredentialCache(w)
	var req struct {
		StoreID     string `json:"store_id"`
		DisplayName string `json:"display_name"`
		Timezone    string `json:"timezone"`
		Code        string `json:"mfa_code"`
	}
	if err := decodeStrictBody(r, 8192, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	p, ok := PrincipalOf(r)
	if !ok || !p.AllStores {
		WriteError(w, r, apperr.New(apperr.Forbidden, "FORBIDDEN"))
		return
	}
	registration := store.RegistrationRequest{StoreID: req.StoreID, DisplayName: req.DisplayName, Timezone: req.Timezone}
	if err := store.ValidateRegistration(registration); err != nil {
		WriteError(w, r, err)
		return
	}
	creator, ok := h.Stores.(StoreCreator)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unavailable, "store creation unavailable"))
		return
	}
	if !h.approveDeviceAction(w, r, req.Code, "store.create") {
		return
	}
	created, err := creator.CreateStore(r.Context(), registration)
	if err != nil {
		h.managementAudit(r, "store.create", "failure", "", req.StoreID)
		WriteError(w, r, err)
		return
	}
	h.managementAudit(r, "store.create", "success", "", created.ID)
	writeJSON(w, http.StatusCreated, storeRow{StoreID: created.ID, DisplayName: created.DisplayName, Timezone: created.Timezone, Status: created.Status, CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt})
}
func (h *DashboardDeviceHandlers) CreateDevice(w http.ResponseWriter, r *http.Request) {
	noCredentialCache(w)
	var req struct {
		Name    string `json:"name"`
		StoreID string `json:"store_id"`
		Code    string `json:"mfa_code"`
	}
	if err := decodeStrictBody(r, 8192, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	id, err := uuid.Parse(req.StoreID)
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid store_id"))
		return
	}
	req.StoreID = id.String()
	if !RequireStore(w, r, req.StoreID) {
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(strings.TrimSpace(req.Name)) > 200 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid device name"))
		return
	}
	if h.Manager == nil {
		WriteError(w, r, apperr.New(apperr.Unavailable, "device management unavailable"))
		return
	}
	if !h.approveDeviceAction(w, r, req.Code, "device.create") {
		return
	}
	provisioned, err := h.Manager.CreateForStore(r.Context(), req.Name, req.StoreID)
	if err != nil {
		h.managementAudit(r, "device.create", "failure", "", req.StoreID)
		WriteError(w, r, err)
		return
	}
	h.managementAudit(r, "device.create", "success", provisioned.Device.ID, req.StoreID)
	h.provisionResponse(w, provisioned, req.StoreID, http.StatusCreated)
}
func (h *DashboardDeviceHandlers) RotateDevice(w http.ResponseWriter, r *http.Request) {
	h.changeDevice(w, r, true)
}
func (h *DashboardDeviceHandlers) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	h.changeDevice(w, r, false)
}
func (h *DashboardDeviceHandlers) changeDevice(w http.ResponseWriter, r *http.Request, rotate bool) {
	noCredentialCache(w)
	var req struct {
		Code string `json:"mfa_code"`
	}
	if err := decodeStrictBody(r, 8192, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	id := r.PathValue("device_id")
	storeID, ok := h.managedStore(w, r, id, !rotate)
	if !ok {
		return
	}
	if h.Manager == nil {
		WriteError(w, r, apperr.New(apperr.Unavailable, "device management unavailable"))
		return
	}
	action := "device.revoke"
	if rotate {
		action = "device.credentials.rotate"
	}
	if !h.approveDeviceAction(w, r, req.Code, action) {
		return
	}
	if rotate {
		p, err := h.Manager.Rotate(r.Context(), id)
		if err != nil {
			h.managementAudit(r, action, "failure", id, storeID)
			WriteError(w, r, err)
			return
		}
		h.managementAudit(r, action, "success", id, storeID)
		h.provisionResponse(w, p, storeID, http.StatusOK)
	} else {
		if err := h.Manager.RevokeDevice(r.Context(), id); err != nil {
			h.managementAudit(r, action, "failure", id, storeID)
			WriteError(w, r, err)
			return
		}
		h.managementAudit(r, action, "success", id, storeID)
		writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
	}
}
