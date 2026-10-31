package catalogadmin

import (
	"context"
	"time"
)

// Command target kinds.
const (
	// TargetKindEntity addresses a pre-existing business entity.
	TargetKindEntity = "entity"
	// TargetKindCreate is a creation intent: no business identity exists
	// yet. EntityID is the explicit absent marker ""; the requested
	// stable key travels in RequestedKey and the Retail-minted identity
	// returns in ResultEntityID. Never a placeholder UUID.
	TargetKindCreate = "create"
)

// CommandView is one command row for API/UI.
type CommandView struct {
	ID               string       `json:"id"`
	StoreID          string       `json:"store_id"`
	Type             string       `json:"type"`
	Version          int          `json:"version"`
	EntityID         string       `json:"entity_id"`
	TargetKind       string       `json:"target_kind"`
	RequestedKey     string       `json:"requested_key,omitempty"`
	ResultEntityID   string       `json:"result_entity_id,omitempty"`
	Payload          []byte       `json:"payload,omitempty"`
	PayloadHash      string       `json:"payload_hash"`
	ExpectedRevision int64        `json:"expected_revision"`
	Actor            string       `json:"actor"`
	Status           string       `json:"status"`
	Aggregate        string       `json:"aggregate"`
	Converged        bool         `json:"converged"`
	Targets          []TargetView `json:"targets,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// TargetView is one per-device target row.
type TargetView struct {
	ID           string `json:"id"`
	CommandID    string `json:"command_id"`
	DeviceID     string `json:"device_id"`
	DeviceName   string `json:"device_name,omitempty"`
	Status       string `json:"status"`
	ResultCode   string `json:"result_code,omitempty"`
	EntityID     string `json:"entity_id,omitempty"`
	PreRevision  int64  `json:"pre_revision"`
	PostRevision int64  `json:"post_revision"`
	Capable      *bool  `json:"capable,omitempty"`
}

// DueTarget is one deliverable target for a polling device.
type DueTarget struct {
	TargetID    string
	CommandID   string
	DeviceID    string
	Status      string
	Type        string
	Version     int
	StoreID     string
	EntityID    string
	Payload     []byte
	PayloadHash string
}

// BoundDevice is one currently Store-bound device.
type BoundDevice struct {
	DeviceID string
	Name     string
}

// DeviceState is the batched device read used when annotating command
// targets (Phase 19): one row per device, no per-device queries.
type DeviceState struct {
	DeviceID string
	Name     string
	Active   bool
	StoreID  string
	Capable  bool
}

// AdminProductRow is one Store-scoped Product for the operator list.
// Phase 17-R0 (ADR-0049): products carry NO sku/stock — they carry
// variant_count and derived_stock (SUM of active, non-tombstoned variant
// stock; product stock is derived only). Variant rows own the SKU.
type AdminProductRow struct {
	ProductID string `json:"product_id"`
	// Phase 17-R2: structural type identity (never inferred).
	ProductTypeID         string `json:"product_type_id"`
	NameAR                string `json:"name_ar"`
	NameEN                string `json:"name_en"`
	IsActive              bool   `json:"is_active"`
	CatalogRevision       int64  `json:"catalog_revision"`
	SellOnline            bool   `json:"sell_online"`
	VariantCount          int64  `json:"variant_count"`
	DerivedStock          int64  `json:"derived_stock"`
	ConfigurationRevision int64  `json:"configuration_revision"`
	HasPending            bool   `json:"has_pending"`
	// ThumbnailSHA256 names the list thumbnail (ADR-0056): the first stored
	// file of the Product gallery, else of a Variant gallery; "" when none.
	ThumbnailSHA256 string `json:"thumbnail_sha256"`
}

// AdminProductDetail is the full projection snapshot an editor needs:
// current values, all three revision streams, and the derived variant
// rollup (variant_count/derived_stock; no product SKU — 17-R0).
type AdminProductDetail struct {
	ProductID string `json:"product_id"`
	// Phase 17-R2: structural type identity (never inferred).
	ProductTypeID         string   `json:"product_type_id"`
	NameAR                string   `json:"name_ar"`
	NameEN                string   `json:"name_en"`
	DescriptionAR         string   `json:"description_ar"`
	DescriptionEN         string   `json:"description_en"`
	WidthCM               *int     `json:"width_cm"`
	HeightCM              *int     `json:"height_cm"`
	TopCategoryID         string   `json:"top_category_id"`
	SubcategoryIDs        []string `json:"subcategory_ids"`
	TagIDs                []string `json:"tag_ids"`
	EGPPriceMinor         string   `json:"egp_price_minor"`
	USDPriceMinor         *string  `json:"usd_price_minor"`
	CostMinor             string   `json:"cost_minor"`
	IsActive              bool     `json:"is_active"`
	SellOnline            bool     `json:"sell_online"`
	SellOffline           bool     `json:"sell_offline"`
	VariantCount          int64    `json:"variant_count"`
	DerivedStock          int64    `json:"derived_stock"`
	CatalogRevision       int64    `json:"catalog_revision"`
	SalesPolicyRevision   int64    `json:"sales_policy_revision"`
	ConfigurationRevision int64    `json:"configuration_revision"`
	HasPending            bool     `json:"has_pending"`
	// Variants are the product's Phase 17 SKU/inventory-owning rows with
	// attributes, prices and last-known stock.
	Variants []AdminProductVariant `json:"variants"`
	// Images are the Product gallery and every Variant gallery (ADR-0056),
	// Product gallery first. Available is false until the file arrives.
	Images []AdminProductImage `json:"images"`
}

// AdminProductImage is one gallery entry; VariantID "" is the Product
// gallery.
type AdminProductImage struct {
	ImageID   string `json:"image_id"`
	VariantID string `json:"variant_id"`
	Position  int    `json:"position"`
	IsPrimary bool   `json:"is_primary"`
	SHA256    string `json:"sha256"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Available bool   `json:"available"`
}

// AdminMedia is one stored image file served to the operator UI.
type AdminMedia struct {
	ContentType string
	Content     []byte
}

// ImageReader reads one stored file's verified bytes (ADR-0057).
type ImageReader interface {
	ReadImage(ctx context.Context, sha256 string) (AdminMedia, error)
}

// AdminCategoryRow is one Store-scoped Category with DAG position.
type AdminCategoryRow struct {
	CategoryID      string   `json:"category_id"`
	NameAR          string   `json:"name_ar"`
	NameEN          string   `json:"name_en"`
	Status          string   `json:"status"`
	OnlineEnabled   bool     `json:"online_enabled"`
	ParentIDs       []string `json:"parent_ids"`
	CatalogRevision int64    `json:"catalog_revision"`
	HasPending      bool     `json:"has_pending"`
}

// AdminTagRow is one Store-scoped Tag.
type AdminTagRow struct {
	TagID           string `json:"tag_id"`
	Slug            string `json:"slug"`
	NameAR          string `json:"name_ar"`
	NameEN          string `json:"name_en"`
	IsActive        bool   `json:"is_active"`
	CatalogRevision int64  `json:"catalog_revision"`
	HasPending      bool   `json:"has_pending"`
}

// AdminConfigurationRow is one projected frame option for the editor.
type AdminConfigurationRow struct {
	ID            string  `json:"id"`
	StyleCode     string  `json:"style_code"`
	StyleNameAR   string  `json:"style_name_ar"`
	StyleNameEN   *string `json:"style_name_en"`
	ColorCode     string  `json:"color_code"`
	ColorNameAR   string  `json:"color_name_ar"`
	ColorNameEN   *string `json:"color_name_en"`
	EGPDeltaMinor string  `json:"egp_delta_minor"`
	USDDeltaMinor *string `json:"usd_delta_minor"`
	Enabled       bool    `json:"enabled"`
	Position      int     `json:"position"`
}

// AdminProductVariantAttribute is one projected physical option of a
// variant with its bilingual display labels (projection-only display).
type AdminProductVariantAttribute struct {
	DefinitionCode   string `json:"definition_code"`
	ValueCode        string `json:"value_code"`
	NameAR           string `json:"name_ar"`
	NameEN           string `json:"name_en"`
	DefinitionNameAR string `json:"definition_name_ar"`
	DefinitionNameEN string `json:"definition_name_en"`
	Position         int    `json:"position"`
}

// AdminProductVariant is one projected product variant for the editor:
// identity, exact minor-unit prices, last-known stock, and options.
// Tombstones keep their rows (deleted=true) for historical safety.
type AdminProductVariant struct {
	VariantID         string                         `json:"variant_id"`
	ProductID         string                         `json:"product_id"`
	SKU               string                         `json:"sku"`
	IsActive          bool                           `json:"is_active"`
	Deleted           bool                           `json:"deleted"`
	PriceEGPMinor     string                         `json:"price_egp_minor"`
	PriceUSDMinor     *string                        `json:"price_usd_minor"`
	StockQuantity     int64                          `json:"stock_quantity"`
	Position          int                            `json:"position"`
	CombinationKey    string                         `json:"combination_key"`
	VariantRevision   int64                          `json:"variant_revision"`
	CatalogRevision   int64                          `json:"catalog_revision"`
	InventoryRevision int64                          `json:"inventory_revision"`
	HasPending        bool                           `json:"has_pending"`
	Attributes        []AdminProductVariantAttribute `json:"attributes"`
}

// Store persists commands, targets and capabilities plus projection
// ownership/convergence reads. Implemented in adapter/postgres.
type Store interface {
	CreateCatalogAdminCommandWithTargets(ctx context.Context, cmd NewCommand) (CommandView, error)
	CreateCatalogAdminCommand(ctx context.Context, cmd NewCommand) (CommandView, error)
	CreateCatalogAdminTarget(ctx context.Context, commandID, targetID, deviceID string) (TargetView, error)
	GetCatalogAdminCommand(ctx context.Context, id string) (CommandView, error)
	ListCatalogAdminCommands(ctx context.Context, storeID, commandType, entityID, status string, limit int, cursorTS time.Time, cursorID string) ([]CommandView, error)
	ListCatalogAdminTargets(ctx context.Context, commandID string) ([]TargetView, error)
	ListCatalogAdminTargetsBatch(ctx context.Context, commandIDs []string) (map[string][]TargetView, error)
	DueCatalogAdminTargets(ctx context.Context, deviceID string, limit int) ([]DueTarget, error)
	FinishCatalogAdminTarget(ctx context.Context, targetID, deviceID, status, code, entityID string, pre, post int64) (bool, error)
	// SetCommandResultEntity records a command's actual resulting entity
	// (Retail-minted on APPLIED). First writer wins; intent never moves.
	SetCommandResultEntity(ctx context.Context, targetID, entityID string) error
	MarkCatalogAdminTargetDelivered(ctx context.Context, targetID, deviceID string) error
	CancelCatalogAdminCommand(ctx context.Context, commandID string) (bool, error)
	ReplaceCatalogAdminCapabilities(ctx context.Context, deviceID string, capabilities []string) error
	CatalogAdminStorePermissions(ctx context.Context, storeID string) (StorePermissions, error)
	GetCatalogAdminCapability(ctx context.Context, deviceID string) (bool, error)
	ListCatalogAdminBoundDevices(ctx context.Context, storeID string) ([]BoundDevice, error)
	// CatalogAdminOwnership verifies projection ownership and reads
	// the stream convergence revision. found=false means unknown
	// entity; storeID "" means legacy NULL (never mutable by Store).
	CatalogAdminOwnership(ctx context.Context, commandType, entityID, requestedStoreID string) (storeID string, revision int64, found bool, err error)
	// CatalogAdminDeviceStates is the Phase 19 batched device read.
	CatalogAdminDeviceStates(ctx context.Context, deviceIDs []string) (map[string]DeviceState, error)
	// Admin reads serve the operator UI from projections only.
	AdminProductList(ctx context.Context, storeID, search, cursor string, limit int) ([]AdminProductRow, error)
	AdminProductDetail(ctx context.Context, storeID, productID string) (AdminProductDetail, error)
	AdminProductConfigurations(ctx context.Context, storeID, productID string) ([]AdminConfigurationRow, error)
	AdminCategoryList(ctx context.Context, storeID string) ([]AdminCategoryRow, error)
	AdminTagList(ctx context.Context, storeID string) ([]AdminTagRow, error)
	// Phase 17 variant reads (Store-scoped, projection only).
	AdminProductVariants(ctx context.Context, storeID, productID string) ([]AdminProductVariant, error)
	AdminProductVariant(ctx context.Context, storeID, variantID string) (AdminProductVariant, error)
	// Phase 17-R2 type reads (Store-scoped, projection only).
	AdminProductTypeList(ctx context.Context, storeID string) ([]AdminProductType, error)
	AdminProductType(ctx context.Context, storeID, typeID string) (AdminProductType, error)
	// ADR-0056 image scope: true only when the file is stored and a gallery of
	// one of the Store's Products names it.
	AdminMediaAllowed(ctx context.Context, storeID, sha256 string) (bool, error)
}

// AdminProductType is one projected structural type for the editor:
// identity, labels, lifecycle, allowed dimensions and capabilities.
type AdminProductType struct {
	TypeID        string   `json:"type_id"`
	Code          string   `json:"code"`
	NameAR        string   `json:"name_ar"`
	NameEN        string   `json:"name_en"`
	DescriptionAR *string  `json:"description_ar,omitempty"`
	DescriptionEN *string  `json:"description_en,omitempty"`
	IsActive      bool     `json:"is_active"`
	Position      int      `json:"position"`
	TypeRevision  int64    `json:"type_revision"`
	Dimensions    []string `json:"dimensions"`
	Capabilities  []string `json:"capabilities"`
	HasPending    bool     `json:"has_pending"`
}

// DeviceDirectory resolves device bindings and lifecycle.
type DeviceDirectory interface {
	// BindingStore returns the Store the device is currently bound to,
	// or "" when unbound.
	BindingStore(ctx context.Context, deviceID string) (string, error)
	// DeviceActive reports the existing device lifecycle.
	DeviceActive(ctx context.Context, deviceID string) (bool, error)
	// DeviceName returns display metadata (best effort).
	DeviceName(ctx context.Context, deviceID string) string
}
