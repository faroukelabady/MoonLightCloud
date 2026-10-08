package fleetupdate

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// cursorState binds a keyset position to the listing kind and Store
// scope that produced it, so a cursor can never be replayed against a
// different Store or listing (prompt §104).
type cursorState struct {
	Kind  string    `json:"k"`
	Scope string    `json:"s"`
	At    time.Time `json:"a,omitempty"`
	ID    string    `json:"i"`
}

func encodeCursor(c cursorState) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(cursor, kind, scope string) (cursorState, error) {
	if cursor == "" {
		return cursorState{}, nil
	}
	if len(cursor) > 512 {
		return cursorState{}, apperr.New(apperr.InvalidInput, "invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorState{}, apperr.New(apperr.InvalidInput, "invalid cursor")
	}
	var c cursorState
	if err := json.Unmarshal(raw, &c); err != nil || c.Kind != kind || c.Scope != scope || (c.ID != "" && !ValidUUID(c.ID)) {
		return cursorState{}, apperr.New(apperr.InvalidInput, "invalid cursor")
	}
	return c, nil
}
