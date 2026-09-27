package orders

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// CommerceOrderProvider is the separate order-read capability providers
// may implement. The frozen CommerceProvider interface is untouched:
// Woo implements both; registry callers assert this capability with
// AsOrderProvider (typed failure, never panic).
type CommerceOrderProvider interface {
	GetOrder(ctx context.Context, externalOrderID string) (OrderSnapshot, error)
}

// AsOrderProvider resolves the order capability from a registered
// provider instance.
func AsOrderProvider(provider commerce.CommerceProvider) (CommerceOrderProvider, error) {
	if provider == nil {
		return nil, apperr.New(apperr.InvalidInput, "nil provider has no order capability")
	}
	orderProvider, ok := provider.(CommerceOrderProvider)
	if !ok {
		return nil, apperr.New(apperr.Unprocessable, fmt.Sprintf("provider %q does not support order reads", provider.Key()))
	}
	return orderProvider, nil
}

// Fingerprint builds the deterministic semantic identity of a normalized
// order: status, money, fulfillment customer/address data, line contents
// with product resolution, and provider modified time. Delivery identity,
// retry metadata, and processing timestamps are excluded, so unrelated
// operational noise never creates a domain revision. Canonical
// length-prefixed serialization; no unordered JSON.
func Fingerprint(snapshot OrderSnapshot) [32]byte {
	h := sha256.New()
	write := func(field string) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		h.Write(length[:])
		h.Write([]byte(field))
	}
	write(snapshot.ProviderKey)
	write(snapshot.ExternalOrderID)
	write(snapshot.OrderNumber)
	write(snapshot.ProviderStatus)
	write(string(snapshot.Canonical))
	writeBool(h, snapshot.ProviderDeleted)
	write(snapshot.Currency)
	writeInt(h, snapshot.DiscountMinor)
	writeInt(h, snapshot.ShippingMinor)
	writeInt(h, snapshot.CartTaxMinor)
	writeInt(h, snapshot.TotalTaxMinor)
	writeInt(h, snapshot.TotalMinor)
	writeBool(h, snapshot.PricesIncludeTax)
	writeTime(h, snapshot.CreatedAt)
	writeTime(h, snapshot.ModifiedAt)
	writeOptionalTime(h, snapshot.PaidAt)
	writeOptionalTime(h, snapshot.CompletedAt)
	write(snapshot.PaymentMethod)
	write(snapshot.PaymentMethodTitle)
	write(snapshot.Customer.FirstName)
	write(snapshot.Customer.LastName)
	write(snapshot.Customer.Email)
	write(snapshot.Customer.Phone)
	writeAddress(h, write, snapshot.Billing)
	writeAddress(h, write, snapshot.Shipping)
	lines := append([]OrderLine(nil), snapshot.Lines...)
	sort.Slice(lines, func(i, j int) bool { return lines[i].ExternalLineID < lines[j].ExternalLineID })
	for _, line := range lines {
		writeInt(h, line.ExternalLineID)
		write(line.ExternalProductID)
		writeInt(h, line.VariationID)
		write(line.SKU)
		write(line.Name)
		writeInt(h, line.Quantity)
		writeInt(h, line.SubtotalMinor)
		writeInt(h, line.SubtotalTaxMinor)
		writeInt(h, line.TotalMinor)
		writeInt(h, line.TotalTaxMinor)
		if line.MoonlightProduct == nil {
			write("")
		} else {
			write(*line.MoonlightProduct)
		}
		writeBool(h, line.Mapped)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func writeInt(h hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	h.Write(encoded[:])
}

func writeBool(h hash.Hash, value bool) {
	if value {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
}

func writeTime(h hash.Hash, value time.Time) {
	h.Write([]byte(value.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")))
}

func writeOptionalTime(h hash.Hash, value *time.Time) {
	if value == nil {
		h.Write([]byte{0})
		return
	}
	h.Write([]byte{1})
	writeTime(h, *value)
}

func writeAddress(h hash.Hash, write func(string), address Address) {
	write(address.Kind)
	write(address.FirstName)
	write(address.LastName)
	write(address.Company)
	write(address.Address1)
	write(address.Address2)
	write(address.City)
	write(address.State)
	write(address.Postcode)
	write(address.Country)
	write(address.Email)
	write(address.Phone)
}

// ValidateDeliveryID enforces non-empty bounded safe delivery identity.
// It is transport identity, never order identity.
func ValidateDeliveryID(value string) error {
	if len(value) == 0 || len(value) > 200 {
		return fmt.Errorf("delivery id must be 1..200 characters")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return fmt.Errorf("delivery id must be printable")
		}
	}
	return nil
}

// CanonicalExternalOrderID normalizes a provider order identity to
// canonical decimal.
func CanonicalExternalOrderID(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	id, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("invalid external order id %q", raw)
	}
	return strconv.FormatInt(id, 10), nil
}
