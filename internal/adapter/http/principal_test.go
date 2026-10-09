package http

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// ownerRequest builds a request carrying an authenticated all-Stores OWNER
// principal, as HumanAuth.Guard would attach in production. Handler unit
// tests use it to exercise handlers directly.
func ownerRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	a := humanauth.Authenticated{Principal: humanauth.Principal{UserID: "00000000-0000-4000-8000-0000000000aa",
		Login: "owner@test.example", Role: humanauth.RoleOwner, AllStores: true}}
	return req.WithContext(context.WithValue(req.Context(), ctxAuthKey{}, a))
}
