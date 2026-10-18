package shopify

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (h *shopifyHarness) opProductCreate(vars map[string]any) map[string]any {
	input, _ := vars["input"].(map[string]any)
	h.createCount++
	id := h.allocateID()
	product := &fakeProduct{
		gid:        "gid://shopify/Product/" + id,
		status:     str(input["status"]),
		title:      str(input["title"]),
		desc:       str(input["descriptionHtml"]),
		metafields: map[string]string{},
	}
	variantID := h.allocateID()
	variant := &fakeVariant{
		gid:        "gid://shopify/ProductVariant/" + variantID,
		itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
		tracked:    true,
		levels:     map[string]int64{},
		metafields: map[string]string{},
	}
	if variants, ok := input["variants"].([]any); ok && len(variants) == 1 {
		if v, ok := variants[0].(map[string]any); ok {
			variant.sku = str(v["sku"])
			variant.price = str(v["price"])
			h.applyVariantMetafields(variant, v)
		}
	}
	if variants, ok := input["variants"].([]any); ok && len(variants) > 1 {
		// Phase 17 multi-variant create: one tracked variant per entry.
		product.variants = nil
		for _, raw := range variants {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			created := &fakeVariant{
				gid:        "gid://shopify/ProductVariant/" + h.allocateID(),
				itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
				tracked:    true,
				levels:     map[string]int64{},
				metafields: map[string]string{},
				sku:        str(entry["sku"]),
				price:      str(entry["price"]),
			}
			h.applyVariantMetafields(created, entry)
			product.variants = append(product.variants, created)
		}
	}
	if metafields, ok := input["metafields"].([]any); ok {
		for _, raw := range metafields {
			if mf, ok := raw.(map[string]any); ok {
				product.metafields[str(mf["namespace"])+"."+str(mf["key"])] = str(mf["value"])
			}
		}
	}
	// create-time list fields: a non-empty tags/collections/media value
	// would be a contract violation for MoonLight creates; record it.
	if _, ok := input["tags"]; ok {
		product.metafields["__violation.tags"] = "sent"
	}
	if _, ok := input["collectionsToJoin"]; ok {
		product.metafields["__violation.collections"] = "sent"
	}
	if product.variants == nil {
		product.variants = []*fakeVariant{variant}
	}
	nodes := []any{}
	for _, created := range product.variants {
		nodes = append(nodes, map[string]any{"id": created.gid, "sku": created.sku})
	}
	h.products[id] = product
	return map[string]any{"data": map[string]any{"productSet": map[string]any{
		"product": map[string]any{
			"id":       product.gid,
			"variants": map[string]any{"nodes": nodes},
		},
		"userErrors": []any{},
	}}}
}

// opVariantSet converges one product's explicit variant set (Phase 17):
// entries with an id update the owned variant, entries without create a
// new tracked variant, and unlisted variants are removed (the same
// explicit-set semantics the frame component relies on).
func (h *shopifyHarness) opVariantSet(vars map[string]any) map[string]any {
	fail := func(message string) map[string]any {
		return map[string]any{"data": map[string]any{"productSet": map[string]any{
			"product": nil, "userErrors": []any{map[string]any{"message": message}}}}}
	}
	input, _ := vars["input"].(map[string]any)
	productID, err := ParseGID(str(input["id"]), ResourceProduct)
	if err != nil {
		return fail("product not found")
	}
	product := h.products[productID]
	if product == nil {
		return fail("product not found")
	}
	byID := map[string]*fakeVariant{}
	for _, existing := range product.variants {
		byID[strings.TrimPrefix(existing.gid, "gid://shopify/ProductVariant/")] = existing
	}
	entries, _ := input["variants"].([]any)
	kept := []*fakeVariant{}
	nodes := []any{}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		target := &fakeVariant{
			gid:        "gid://shopify/ProductVariant/" + h.allocateID(),
			itemGID:    "gid://shopify/InventoryItem/" + h.allocateID(),
			tracked:    true,
			levels:     map[string]int64{},
			metafields: map[string]string{},
		}
		if id := str(entry["id"]); id != "" {
			decimal, err := ParseGID(id, ResourceProductVariant)
			if err != nil {
				return fail("variant identity malformed")
			}
			existing := byID[decimal]
			if existing == nil {
				return fail("variant not found")
			}
			target = existing
		}
		if sku := str(entry["sku"]); sku != "" {
			target.sku = sku
		}
		if price := str(entry["price"]); price != "" {
			target.price = price
		}
		h.applyVariantMetafields(target, entry)
		kept = append(kept, target)
		nodes = append(nodes, map[string]any{"id": target.gid, "name": target.sku})
	}
	product.variants = kept
	options := []any{}
	if raw, ok := input["productOptions"].([]any); ok {
		for _, entry := range raw {
			option := entry.(map[string]any)
			options = append(options, map[string]any{
				"id":   "gid://shopify/ProductOption/" + h.allocateID(),
				"name": str(option["name"]),
			})
		}
	}
	return map[string]any{"data": map[string]any{"productSet": map[string]any{
		"product": map[string]any{
			"id":       product.gid,
			"options":  options,
			"variants": map[string]any{"nodes": nodes},
		},
		"userErrors": []any{},
	}}}
}

func (h *shopifyHarness) opProductUpdate(vars map[string]any) map[string]any {
	input, _ := vars["input"].(map[string]any)
	id, _ := ParseGID(str(input["id"]), ResourceProduct)
	product := h.products[id]
	if product == nil {
		return map[string]any{"data": map[string]any{"productUpdate": map[string]any{
			"product": nil, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	product.title = str(input["title"])
	product.desc = str(input["descriptionHtml"])
	if status := str(input["status"]); status != "" {
		product.status = status
	}
	// Manual-field preservation guard: any managed-foreign field in the
	// update input is a contract violation.
	for _, forbidden := range []string{"tags", "vendor", "productType", "collectionsToJoin", "collectionsToLeave", "media", "seo", "category"} {
		if _, ok := input[forbidden]; ok {
			product.metafields["__violation."+forbidden] = "sent"
		}
	}
	return map[string]any{"data": map[string]any{"productUpdate": map[string]any{
		"product": map[string]any{"id": product.gid}, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opVariantUpdate(vars map[string]any) map[string]any {
	productID, _ := ParseGID(str(vars["productId"]), ResourceProduct)
	product := h.products[productID]
	if product == nil {
		return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
			"productVariants": []any{}, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	list, _ := vars["variants"].([]any)
	updated := []any{}
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		variantID, err := ParseGID(str(entry["id"]), ResourceProductVariant)
		if err != nil {
			continue
		}
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.gid, variantID) {
				item, _ := entry["inventoryItem"].(map[string]any)
				variant.sku = str(item["sku"])
				variant.price = str(entry["price"])
				updated = append(updated, map[string]any{"id": variant.gid, "sku": variant.sku})
			}
		}
	}
	if len(list) != 1 {
		// Listing zero or many variants would delete/unmanaged-edit
		// manual variants: record the violation.
		product.metafields["__violation.variant_list"] = strconv.Itoa(len(list))
	}
	return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
		"productVariants": updated, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opMetafieldsSet(vars map[string]any) map[string]any {
	list, _ := vars["metafields"].([]any)
	written := []any{}
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		ownerGID := str(entry["ownerId"])
		key := str(entry["namespace"]) + "." + str(entry["key"])
		value := str(entry["value"])
		productID, err := ParseGID(ownerGID, ResourceProduct)
		if err == nil {
			product := h.products[productID]
			if product == nil {
				continue
			}
			product.metafields[key] = value
			written = append(written, map[string]any{
				"namespace": str(entry["namespace"]), "key": str(entry["key"])})
			continue
		}
		// Phase 17: variant-level ownership/metafields (moonlight.variant_id
		// and the per-variant inventory revision fence).
		variantID, err := ParseGID(ownerGID, ResourceProductVariant)
		if err != nil {
			continue
		}
		if !h.applyVariantMetafieldGID(variantID, key, value) {
			continue
		}
		written = append(written, map[string]any{
			"namespace": str(entry["namespace"]), "key": str(entry["key"])})
	}
	return map[string]any{"data": map[string]any{"metafieldsSet": map[string]any{
		"metafields": written, "userErrors": []any{}}}}
}

// applyVariantMetafields copies create/update inline metafields onto one
// fake variant.
func (h *shopifyHarness) applyVariantMetafields(variant *fakeVariant, entry map[string]any) {
	list, _ := entry["metafields"].([]any)
	for _, raw := range list {
		mf, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if variant.metafields == nil {
			variant.metafields = map[string]string{}
		}
		variant.metafields[str(mf["namespace"])+"."+str(mf["key"])] = str(mf["value"])
	}
}

// applyVariantMetafieldGID writes one metafield on the variant with the
// given decimal identity.
func (h *shopifyHarness) applyVariantMetafieldGID(variantID, key, value string) bool {
	for _, product := range h.products {
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.gid, variantID) {
				if variant.metafields == nil {
					variant.metafields = map[string]string{}
				}
				variant.metafields[key] = value
				return true
			}
		}
	}
	return false
}

func (h *shopifyHarness) opPublish(vars map[string]any, publish bool) map[string]any {
	productID, _ := ParseGID(str(vars["id"]), ResourceProduct)
	inputs, _ := vars["input"].([]any)
	publicationID := ""
	if len(inputs) == 1 {
		input, _ := inputs[0].(map[string]any)
		publicationID = str(input["publicationId"])
	}
	product := h.products[productID]
	if product == nil {
		return map[string]any{"data": map[string]any{"publishablePublish": map[string]any{
			"publishable": nil, "userErrors": []any{map[string]any{"message": "product not found"}}}}}
	}
	if publicationID == h.publicationGID {
		h.published[productID] = publish
	} else {
		h.publishedElsewhere[productID] = true
	}
	key := "publishablePublish"
	if !publish {
		key = "publishableUnpublish"
	}
	return map[string]any{"data": map[string]any{key: map[string]any{
		"publishable": map[string]any{"id": product.gid}, "userErrors": []any{}}}}
}

func (h *shopifyHarness) opInventoryActivate(vars map[string]any) map[string]any {
	key := str(vars["idempotencyKey"])
	h.idemKeys["activate"] = append(h.idemKeys["activate"], key)
	if h.idemApplied[key] {
		// Shopify idempotent replay: same key returns the original
		// success without re-applying the mutation.
		return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
			"inventoryLevel": map[string]any{"id": "gid://shopify/InventoryLevel/replay"},
			"userErrors":     []any{}}}}
	}
	h.idemApplied[key] = true
	itemID, _ := ParseGID(str(vars["inventoryItemId"]), ResourceInventoryItem)
	locationID := str(vars["locationId"])
	for _, product := range h.products {
		for _, variant := range product.variants {
			if strings.HasSuffix(variant.itemGID, itemID) {
				if _, ok := variant.levels[locationID]; !ok {
					variant.levels[locationID] = 0
				}
				return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
					"inventoryLevel": map[string]any{"id": "gid://shopify/InventoryLevel/" + h.allocateID()},
					"userErrors":     []any{}}}}
			}
		}
	}
	return map[string]any{"data": map[string]any{"inventoryActivate": map[string]any{
		"inventoryLevel": nil, "userErrors": []any{map[string]any{"message": "item not found"}}}}}
}

func (h *shopifyHarness) opInventorySet(vars map[string]any) map[string]any {
	key := str(vars["idempotencyKey"])
	h.idemKeys["set"] = append(h.idemKeys["set"], key)
	if h.idemApplied[key] {
		return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
			"inventoryAdjustmentGroup": map[string]any{"id": "gid://shopify/InventoryAdjustmentGroup/replay"},
			"userErrors":               []any{}}}}
	}
	h.idemApplied[key] = true
	if h.beforeInventorySet != nil {
		h.beforeInventorySet(h)
		h.beforeInventorySet = nil
	}
	input, _ := vars["input"].(map[string]any)
	if str(input["name"]) != "available" {
		return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
			"inventoryAdjustmentGroup": nil,
			"userErrors":               []any{map[string]any{"code": "INVALID", "message": "unsupported name"}}}}}
	}
	list, _ := input["quantities"].([]any)
	for _, raw := range list {
		entry, _ := raw.(map[string]any)
		itemID, err := ParseGID(str(entry["inventoryItemId"]), ResourceInventoryItem)
		if err != nil {
			continue
		}
		locationID := str(entry["locationId"])
		quantity := int64(0)
		switch v := entry["quantity"].(type) {
		case float64:
			quantity = int64(v)
		case json.Number:
			quantity, _ = strconv.ParseInt(v.String(), 10, 64)
		}
		for _, product := range h.products {
			for _, variant := range product.variants {
				if !strings.HasSuffix(variant.itemGID, itemID) {
					continue
				}
				current, active := variant.levels[locationID]
				if !active {
					return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
						"inventoryAdjustmentGroup": nil,
						"userErrors":               []any{map[string]any{"code": "INVALID", "message": "not activated"}}}}}
				}
				changeRaw, present := entry["changeFromQuantity"]
				if present && changeRaw != nil {
					changeFrom := int64(0)
					switch v := changeRaw.(type) {
					case float64:
						changeFrom = int64(v)
					case json.Number:
						changeFrom, _ = strconv.ParseInt(v.String(), 10, 64)
					}
					if changeFrom != current {
						h.casViolations++
						return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
							"inventoryAdjustmentGroup": nil,
							"userErrors": []any{map[string]any{
								"code": "CONFLICT", "message": "compare quantity mismatch"}}}}}
					}
				}
				variant.levels[locationID] = quantity
				return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
					"inventoryAdjustmentGroup": map[string]any{"id": "gid://shopify/InventoryAdjustmentGroup/" + h.allocateID()},
					"userErrors":               []any{}}}}
			}
		}
	}
	return map[string]any{"data": map[string]any{"inventorySetQuantities": map[string]any{
		"inventoryAdjustmentGroup": nil,
		"userErrors":               []any{map[string]any{"code": "NOT_FOUND", "message": "item not found"}}}}}
}

func (h *shopifyHarness) opVariantsBySKU(vars map[string]any) map[string]any {
	query := str(vars["query"])
	wanted := strings.TrimPrefix(query, "sku:")
	nodes := []any{}
	for _, product := range h.products {
		for _, variant := range product.variants {
			// Search semantics are approximate: prefix matches are
			// returned too, so exact equality filtering is mandatory.
			if strings.HasPrefix(variant.sku, wanted) || strings.HasPrefix(wanted, variant.sku) {
				nodes = append(nodes, map[string]any{
					"id":              variant.gid,
					"sku":             variant.sku,
					"selectedOptions": []any{map[string]any{"name": "Title", "value": "Default Title"}},
					"product": map[string]any{
						"id":         product.gid,
						"status":     product.status,
						"options":    []any{map[string]any{"id": "gid://shopify/ProductOption/" + strings.TrimPrefix(product.gid, "gid://shopify/Product/"), "name": "Title"}},
						"metafields": map[string]any{"nodes": metafieldNodes(product.metafields)},
					},
				})
			}
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return str(nodes[i].(map[string]any)["id"]) < str(nodes[j].(map[string]any)["id"])
	})
	return map[string]any{"data": map[string]any{"productVariants": fixturePage(nodes, vars)}}
}

func (h *shopifyHarness) opProductQuery(vars map[string]any) map[string]any {
	id, err := ParseGID(str(vars["id"]), ResourceProduct)
	if err != nil {
		return map[string]any{"data": map[string]any{"product": nil}}
	}
	product := h.products[id]
	if product == nil {
		return map[string]any{"data": map[string]any{"product": nil}}
	}
	variants := []any{}
	for _, variant := range product.variants {
		levels := []any{}
		for location, quantity := range variant.levels {
			levels = append(levels, map[string]any{
				"location":   map[string]any{"id": location},
				"updatedAt":  time.Unix(h.nextID, 0).UTC().Format(time.RFC3339Nano),
				"quantities": []any{map[string]any{"name": "available", "quantity": quantity}},
			})
		}
		variants = append(variants, map[string]any{
			"id":              variant.gid,
			"sku":             variant.sku,
			"title":           variant.title,
			"selectedOptions": []any{map[string]any{"name": "Title", "value": "Default Title"}},
			"metafields":      map[string]any{"nodes": metafieldNodes(variant.metafields)},
			"inventoryItem": map[string]any{
				"id":              variant.itemGID,
				"tracked":         variant.tracked,
				"inventoryLevels": map[string]any{"nodes": levels},
			},
		})
	}
	return map[string]any{"data": map[string]any{"product": map[string]any{
		"id":         product.gid,
		"status":     product.status,
		"options":    []any{map[string]any{"id": "gid://shopify/ProductOption/" + strings.TrimPrefix(product.gid, "gid://shopify/Product/"), "name": "Title"}},
		"metafields": map[string]any{"nodes": metafieldNodes(product.metafields)},
		"variants":   map[string]any{"nodes": variants},
	}}}
}

func (h *shopifyHarness) opOrder(vars map[string]any) map[string]any {
	id, err := ParseGID(str(vars["id"]), ResourceOrder)
	if err != nil {
		return map[string]any{"data": map[string]any{"order": nil}}
	}
	order := h.orders[id]
	if order == nil {
		return map[string]any{"data": map[string]any{"order": nil}}
	}
	raw := map[string]any{}
	for key, value := range order.raw {
		raw[key] = value
	}
	connection, _ := raw["lineItems"].(map[string]any)
	nodes, _ := connection["nodes"].([]any)
	raw["lineItems"] = fixturePage(nodes, vars)
	if connection["pageInfo"] != nil {
		raw["lineItems"] = connection
	}
	return map[string]any{"data": map[string]any{"order": raw}}
}

func metafieldNodes(values map[string]string) []any {
	nodes := []any{}
	for compound, value := range values {
		if strings.HasPrefix(compound, "__violation") {
			continue
		}
		parts := strings.SplitN(compound, ".", 2)
		nodes = append(nodes, map[string]any{
			"namespace": parts[0], "key": parts[1], "value": value})
	}
	return nodes
}
