package http

import (
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProvisionResponseIsNotCacheable(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&DashboardDeviceHandlers{}).provisionResponse(recorder, auth.Provisioned{Device: auth.Device{ID: "device"}, Credential: auth.Credential{ID: "credential"}, RawSecret: "synthetic-secret"}, "store", 201)
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response cacheable")
	}
	if !strings.Contains(recorder.Body.String(), "device.credential.synthetic-secret") {
		t.Fatal("not a ready-to-paste credential")
	}
}
