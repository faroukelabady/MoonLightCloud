package catalog

// ADR-0056 — Product media snapshot contract. Wire mirror of MoonLightRetail
// internal/domain/sync/catalog_media.go. The snapshot describes the galleries
// only; image bytes are content-addressed uploads (internal/media) that may
// arrive before or after it.

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// EventProductMediaSnapshotV1 is the complete gallery state of one Product
// (its own gallery plus every Variant gallery) at one media revision.
const EventProductMediaSnapshotV1 = "catalog.product.media.snapshot.v1"

// Media bounds shared with Retail and the upload endpoint.
const (
	MaxMediaPerProduct = 200
	MaxMediaBytes      = 5 << 20
	MaxMediaPixels     = 20_000_000
)

// ProductMediaEntry is one image of one gallery. VariantID nil is the
// Product gallery.
type ProductMediaEntry struct {
	ImageID     string  `json:"image_id"`
	VariantID   *string `json:"variant_id"`
	Position    int     `json:"position"`
	IsPrimary   bool    `json:"is_primary"`
	SHA256      string  `json:"sha256"`
	ContentType string  `json:"content_type"`
	ByteSize    int     `json:"byte_size"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
}

// ProductMediaSnapshot is the complete media state of one Product.
type ProductMediaSnapshot struct {
	ProductID     string              `json:"product_id"`
	MediaRevision int64               `json:"media_revision"`
	Images        []ProductMediaEntry `json:"images"`
}

// DecodeProductMediaSnapshot parses canonical payload bytes.
func DecodeProductMediaSnapshot(raw json.RawMessage) (ProductMediaSnapshot, error) {
	var snapshot ProductMediaSnapshot
	if err := decodePayload(EventProductMediaSnapshotV1, raw, &snapshot); err != nil {
		return ProductMediaSnapshot{}, err
	}
	return snapshot, nil
}

// ValidateProductMediaSnapshot enforces the ADR-0056 bounds and returns the
// snapshot in canonical form (lower-case UUIDs, canonical order).
func ValidateProductMediaSnapshot(snapshot ProductMediaSnapshot) (ProductMediaSnapshot, error) {
	invalid := func(reason string) error {
		return fmt.Errorf("invalid %s: %s", EventProductMediaSnapshotV1, reason)
	}
	productID, err := uuid.Parse(snapshot.ProductID)
	if err != nil || len(snapshot.ProductID) != 36 {
		return snapshot, invalid("product_id must be a UUID")
	}
	if snapshot.MediaRevision < 1 {
		return snapshot, invalid("media_revision must be >= 1")
	}
	if snapshot.Images == nil {
		return snapshot, invalid("images is required")
	}
	if len(snapshot.Images) > MaxMediaPerProduct {
		return snapshot, invalid("too many images")
	}
	out := ProductMediaSnapshot{ProductID: productID.String(), MediaRevision: snapshot.MediaRevision,
		Images: make([]ProductMediaEntry, 0, len(snapshot.Images))}
	ids := make(map[string]bool, len(snapshot.Images))
	positions := make(map[string]bool, len(snapshot.Images))
	primaries := make(map[string]bool)
	for _, entry := range snapshot.Images {
		imageID, err := uuid.Parse(entry.ImageID)
		if err != nil || len(entry.ImageID) != 36 {
			return snapshot, invalid("image_id must be a UUID")
		}
		canonical := entry
		canonical.ImageID = imageID.String()
		gallery := ""
		if entry.VariantID != nil {
			variantID, err := uuid.Parse(*entry.VariantID)
			if err != nil || len(*entry.VariantID) != 36 {
				return snapshot, invalid("variant_id must be a UUID or null")
			}
			value := variantID.String()
			canonical.VariantID = &value
			gallery = value
		}
		if ids[canonical.ImageID] {
			return snapshot, invalid("duplicate image_id")
		}
		ids[canonical.ImageID] = true
		if entry.Position < 0 {
			return snapshot, invalid("position must be >= 0")
		}
		slot := fmt.Sprintf("%s/%d", gallery, entry.Position)
		if positions[slot] {
			return snapshot, invalid("duplicate position in one gallery")
		}
		positions[slot] = true
		if entry.IsPrimary {
			if primaries[gallery] {
				return snapshot, invalid("more than one primary image in one gallery")
			}
			primaries[gallery] = true
		}
		if !ValidSHA256(entry.SHA256) {
			return snapshot, invalid("sha256 must be 64 lower-case hex characters")
		}
		if !ValidMediaContentType(entry.ContentType) {
			return snapshot, invalid("content_type must be image/jpeg or image/png")
		}
		if entry.ByteSize < 1 || entry.ByteSize > MaxMediaBytes {
			return snapshot, invalid("byte_size out of bounds")
		}
		if !ValidMediaDimensions(entry.Width, entry.Height) {
			return snapshot, invalid("width and height out of bounds")
		}
		out.Images = append(out.Images, canonical)
	}
	sortProductMedia(out.Images)
	return out, nil
}

// ValidSHA256 accepts exactly 64 lower-case hex characters.
func ValidSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidMediaContentType accepts the two formats Retail stores.
func ValidMediaContentType(value string) bool {
	return value == "image/jpeg" || value == "image/png"
}

// ValidMediaDimensions mirrors Retail's decode bounds.
func ValidMediaDimensions(width, height int) bool {
	return width >= 1 && height >= 1 && width <= MaxMediaPixels/height
}

// sortProductMedia puts the Product gallery first, then Variant galleries by
// ID, each by position: the comparison order for equal revisions.
func sortProductMedia(entries []ProductMediaEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := "", ""
		if entries[i].VariantID != nil {
			a = *entries[i].VariantID
		}
		if entries[j].VariantID != nil {
			b = *entries[j].VariantID
		}
		if a != b {
			return a < b
		}
		return entries[i].Position < entries[j].Position
	})
}

// NormalizeProductMedia returns entries in canonical comparison order.
func NormalizeProductMedia(entries []ProductMediaEntry) []ProductMediaEntry {
	out := append([]ProductMediaEntry{}, entries...)
	sortProductMedia(out)
	return out
}
