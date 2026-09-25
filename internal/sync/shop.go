package sync

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Shop snapshot bounds mirror the authoritative Retail shop-profile domain
// validation (internal/domain/settings ShopProfile.Valid) exactly: only the
// Arabic name is required; every other field is optional with a rune cap.
// Cloud ingestion must accept every structurally valid snapshot Retail is
// allowed to emit — for sales and returns alike — so both validators share
// this helper and cannot drift apart again.
const (
	maxShopNameRunes    = 120
	maxShopAddressRunes = 300
	maxShopPhoneRunes   = 50
	maxShopFooterRunes  = 500
)

// CheckShopSnapshot validates one historical shop identity block against
// the Retail producer contract.
func CheckShopSnapshot(nameAR, nameEN, addressAR, addressEN, phone, footerAR, footerEN string) error {
	bounded := func(value string, max int, field string) error {
		if utf8.RuneCountInString(value) > max {
			return fmt.Errorf("shop.%s must be at most %d chars", field, max)
		}
		return nil
	}
	if strings.TrimSpace(nameAR) == "" {
		return fmt.Errorf("shop.name_ar is required")
	}
	for _, field := range []struct {
		value string
		max   int
		name  string
	}{
		{nameAR, maxShopNameRunes, "name_ar"},
		{nameEN, maxShopNameRunes, "name_en"},
		{addressAR, maxShopAddressRunes, "address_ar"},
		{addressEN, maxShopAddressRunes, "address_en"},
		{phone, maxShopPhoneRunes, "phone"},
		{footerAR, maxShopFooterRunes, "receipt_footer_ar"},
		{footerEN, maxShopFooterRunes, "receipt_footer_en"},
	} {
		if err := bounded(field.value, field.max, field.name); err != nil {
			return err
		}
	}
	return nil
}
