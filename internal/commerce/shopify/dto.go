package shopify

import "encoding/json"

// Static GraphQL documents (Phase 11). Documents are defined here, never
// constructed from caller or merchant input; every dynamic value travels
// as a GraphQL variable (§78/§79 of the Phase 11 contract).

const (
	docShopCurrency = `query MoonlightShopCurrency {
  shop { currencyCode }
}`

	docProductQuery = `query MoonlightProduct($id: ID!) {
  product(id: $id) {
    id
    status
    metafields(first: 20, namespace: "moonlight") { nodes { namespace key value } }
    variants(first: 50) {
      nodes {
        id
        sku
        inventoryItem {
          id
          tracked
          inventoryLevels(first: 20) {
            nodes {
              location { id }
              quantities(names: ["available"]) { name quantity }
            }
          }
        }
      }
    }
  }
}`

	docVariantsBySKU = `query MoonlightVariantsBySKU($query: String!) {
  productVariants(first: 10, query: $query) {
    nodes {
      id
      sku
      product {
        id
        status
        metafields(first: 20, namespace: "moonlight") { nodes { namespace key value } }
      }
    }
  }
}`

	docProductCreate = `mutation MoonlightProductCreate($input: ProductSetInput!) {
  productSet(synchronous: true, input: $input) {
    product { id variants(first: 1) { nodes { id sku } } }
    userErrors { field message }
  }
}`

	docProductUpdate = `mutation MoonlightProductUpdate($input: ProductInput!) {
  productUpdate(input: $input) {
    product { id }
    userErrors { field message }
  }
}`

	docManagedVariantUpdate = `mutation MoonlightManagedVariantUpdate($productId: ID!, $variants: [ProductVariantsBulkInput!]!) {
  productVariantsBulkUpdate(productId: $productId, variants: $variants) {
    productVariants { id sku }
    userErrors { field message }
  }
}`

	docMetafieldsSet = `mutation MoonlightMetafieldsSet($metafields: [MetafieldInput!]!) {
  metafieldsSet(metafields: $metafields) {
    metafields { id namespace key }
    userErrors { field message }
  }
}`

	docPublish = `mutation MoonlightPublish($id: ID!, $publicationId: ID!) {
  publishablePublish(id: $id, publicationId: $publicationId) {
    publishable { id }
    userErrors { field message }
  }
}`

	docUnpublish = `mutation MoonlightUnpublish($id: ID!, $publicationId: ID!) {
  publishableUnpublish(id: $id, publicationId: $publicationId) {
    publishable { id }
    userErrors { field message }
  }
}`

	docInventoryActivate = `mutation MoonlightInventoryActivate($inventoryItemId: ID!, $locationId: ID!, $idempotencyKey: String!) {
  inventoryActivate(inventoryItemId: $inventoryItemId, locationId: $locationId, available: 0) @idempotent(key: $idempotencyKey) {
    inventoryLevel { id }
    userErrors { field message }
  }
}`

	docInventorySet = `mutation MoonlightInventorySet($input: InventorySetQuantitiesInput!, $idempotencyKey: String!) {
  inventorySetQuantities(input: $input) @idempotent(key: $idempotencyKey) {
    inventoryAdjustmentGroup { id }
    userErrors { code field message }
  }
}`

	docOrder = `query MoonlightOrder($id: ID!) {
  order(id: $id) {
    id
    legacyResourceId
    name
    createdAt
    updatedAt
    processedAt
    closedAt
    cancelledAt
    displayFinancialStatus
    displayFulfillmentStatus
    currencyCode
    taxesIncluded
    paymentGatewayNames
    email
    phone
    fullyPaid
    totalPriceSet { shopMoney { amount currencyCode } }
    totalShippingPriceSet { shopMoney { amount currencyCode } }
    totalTaxSet { shopMoney { amount currencyCode } }
    totalDiscountsSet { shopMoney { amount currencyCode } }
    customer { firstName lastName email phone }
    billingAddress { firstName lastName company address1 address2 city provinceCode zip countryCodeV2 phone }
    shippingAddress { firstName lastName company address1 address2 city provinceCode zip countryCodeV2 phone }
    lineItems(first: 50) {
      nodes {
        id
        title
        sku
        quantity
        originalTotalSet { shopMoney { amount currencyCode } }
        discountedTotalSet { shopMoney { amount currencyCode } }
        totalDiscountSet { shopMoney { amount currencyCode } }
        taxLines { title rate priceSet { shopMoney { amount currencyCode } } }
        variant { id }
        product { id }
      }
    }
  }
}`
)

// userError is one GraphQL mutation userError entry. Field is kept raw:
// the API has returned both string and list shapes across versions.
type userError struct {
	Code    string          `json:"code"`
	Field   json.RawMessage `json:"field"`
	Message string          `json:"message"`
}

type gqlMetafield struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
}

type gqlInventoryLevel struct {
	Location struct {
		ID string `json:"id"`
	} `json:"location"`
	Quantities []struct {
		Name     string `json:"name"`
		Quantity int64  `json:"quantity"`
	} `json:"quantities"`
}

type gqlInventoryItem struct {
	ID      string `json:"id"`
	Tracked bool   `json:"tracked"`
	Levels  struct {
		Nodes []gqlInventoryLevel `json:"nodes"`
	} `json:"inventoryLevels"`
}

type gqlVariant struct {
	ID            string            `json:"id"`
	SKU           string            `json:"sku"`
	InventoryItem *gqlInventoryItem `json:"inventoryItem"`
}

type gqlProduct struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Metafields struct {
		Nodes []gqlMetafield `json:"nodes"`
	} `json:"metafields"`
	Variants struct {
		Nodes []gqlVariant `json:"nodes"`
	} `json:"variants"`
}

// gqlVariantWithProduct is the SKU-recovery search row.
type gqlVariantWithProduct struct {
	ID      string `json:"id"`
	SKU     string `json:"sku"`
	Product struct {
		ID         string `json:"id"`
		Status     string `json:"status"`
		Metafields struct {
			Nodes []gqlMetafield `json:"nodes"`
		} `json:"metafields"`
	} `json:"product"`
}

type gqlMoney struct {
	Amount       string `json:"amount"`
	CurrencyCode string `json:"currencyCode"`
}

type gqlMoneyBag struct {
	ShopMoney gqlMoney `json:"shopMoney"`
}

// gqlLineItem is the order line projection the adapter trusts.
type gqlLineItem struct {
	ID                 string      `json:"id"`
	Title              string      `json:"title"`
	SKU                string      `json:"sku"`
	Quantity           int64       `json:"quantity"`
	OriginalTotalSet   gqlMoneyBag `json:"originalTotalSet"`
	DiscountedTotalSet gqlMoneyBag `json:"discountedTotalSet"`
	TaxLines           []struct {
		Title    string      `json:"title"`
		Rate     json.Number `json:"rate"`
		PriceSet gqlMoneyBag `json:"priceSet"`
	} `json:"taxLines"`
	Variant *struct {
		ID string `json:"id"`
	} `json:"variant"`
	Product *struct {
		ID string `json:"id"`
	} `json:"product"`
}

// gqlOrder is the order projection the adapter trusts; every other
// response field is ignored and never persisted.
type gqlOrder struct {
	ID                       string      `json:"id"`
	LegacyResourceID         string      `json:"legacyResourceId"`
	Name                     string      `json:"name"`
	CreatedAt                string      `json:"createdAt"`
	UpdatedAt                string      `json:"updatedAt"`
	ProcessedAt              string      `json:"processedAt"`
	ClosedAt                 string      `json:"closedAt"`
	CancelledAt              string      `json:"cancelledAt"`
	DisplayFinancialStatus   string      `json:"displayFinancialStatus"`
	DisplayFulfillmentStatus string      `json:"displayFulfillmentStatus"`
	CurrencyCode             string      `json:"currencyCode"`
	TaxesIncluded            bool        `json:"taxesIncluded"`
	PaymentGatewayNames      []string    `json:"paymentGatewayNames"`
	Email                    string      `json:"email"`
	Phone                    string      `json:"phone"`
	FullyPaid                bool        `json:"fullyPaid"`
	TotalPriceSet            gqlMoneyBag `json:"totalPriceSet"`
	TotalShippingPriceSet    gqlMoneyBag `json:"totalShippingPriceSet"`
	TotalTaxSet              gqlMoneyBag `json:"totalTaxSet"`
	TotalDiscountsSet        gqlMoneyBag `json:"totalDiscountsSet"`
	Customer                 *struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		Phone     string `json:"phone"`
	} `json:"customer"`
	BillingAddress  *gqlAddress `json:"billingAddress"`
	ShippingAddress *gqlAddress `json:"shippingAddress"`
	LineItems       struct {
		Nodes []gqlLineItem `json:"nodes"`
	} `json:"lineItems"`
}

type gqlAddress struct {
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Company       string `json:"company"`
	Address1      string `json:"address1"`
	Address2      string `json:"address2"`
	City          string `json:"city"`
	ProvinceCode  string `json:"provinceCode"`
	ZIP           string `json:"zip"`
	CountryCodeV2 string `json:"countryCodeV2"`
	Phone         string `json:"phone"`
}

// Response envelopes per operation.

type productCreateResponse struct {
	ProductSet struct {
		Product *struct {
			ID       string `json:"id"`
			Variants struct {
				Nodes []struct {
					ID  string `json:"id"`
					SKU string `json:"sku"`
				} `json:"nodes"`
			} `json:"variants"`
		} `json:"product"`
		UserErrors []userError `json:"userErrors"`
	} `json:"productSet"`
}

type productUpdateResponse struct {
	ProductUpdate struct {
		Product *struct {
			ID string `json:"id"`
		} `json:"product"`
		UserErrors []userError `json:"userErrors"`
	} `json:"productUpdate"`
}

type variantUpdateResponse struct {
	ProductVariantsBulkUpdate struct {
		ProductVariants []struct {
			ID  string `json:"id"`
			SKU string `json:"sku"`
		} `json:"productVariants"`
		UserErrors []userError `json:"userErrors"`
	} `json:"productVariantsBulkUpdate"`
}

type metafieldsSetResponse struct {
	MetafieldsSet struct {
		Metafields []struct {
			Namespace string `json:"namespace"`
			Key       string `json:"key"`
		} `json:"metafields"`
		UserErrors []userError `json:"userErrors"`
	} `json:"metafieldsSet"`
}

type publishResponse struct {
	PublishablePublish *struct {
		Publishable *struct {
			ID string `json:"id"`
		} `json:"publishable"`
		UserErrors []userError `json:"userErrors"`
	} `json:"publishablePublish"`
	PublishableUnpublish *struct {
		Publishable *struct {
			ID string `json:"id"`
		} `json:"publishable"`
		UserErrors []userError `json:"userErrors"`
	} `json:"publishableUnpublish"`
}

type inventoryActivateResponse struct {
	InventoryActivate struct {
		InventoryLevel *struct {
			ID string `json:"id"`
		} `json:"inventoryLevel"`
		UserErrors []userError `json:"userErrors"`
	} `json:"inventoryActivate"`
}

type inventorySetResponse struct {
	InventorySetQuantities struct {
		InventoryAdjustmentGroup *struct {
			ID string `json:"id"`
		} `json:"inventoryAdjustmentGroup"`
		UserErrors []userError `json:"userErrors"`
	} `json:"inventorySetQuantities"`
}

type productQueryResponse struct {
	Product *gqlProduct `json:"product"`
}

type variantsBySKUResponse struct {
	ProductVariants struct {
		Nodes []gqlVariantWithProduct `json:"nodes"`
	} `json:"productVariants"`
}

type orderQueryResponse struct {
	Order *gqlOrder `json:"order"`
}

type shopCurrencyResponse struct {
	Shop struct {
		CurrencyCode string `json:"currencyCode"`
	} `json:"shop"`
}
