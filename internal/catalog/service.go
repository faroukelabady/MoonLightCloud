package catalog

import (
	"context"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Read model for future phases (5B/5C/6A): stable internal IDs stay
// authoritative; no provider mappings, no stock, no HTTP surface. Money
// stays int64 minor units; dimensions stay whole centimeters or nil.

// Price is one projected currency row.
type Price struct {
	Currency   string
	PriceCents int64
	CostCents  *int64
}

// ProductHeader is the list-safe product identity (no relations).
type ProductHeader struct {
	ID            string
	SKU           string
	Name          string
	IsActive      bool
	Revision      int64
	SourceEventID string
}

// Product is the full projected product aggregate.
type Product struct {
	ProductHeader
	Description    *string
	TopCategoryID  string
	WidthCM        *int
	HeightCM       *int
	Prices         []Price
	Translations   []CatalogProductTranslation
	SubcategoryIDs []string
	TagIDs         []string
}

// Category is the projected category with its complete parent set.
type Category struct {
	ID            string
	Status        string
	NameAR        string
	NameEN        *string
	ParentIDs     []string
	Revision      int64
	SourceEventID string
}

// Tag is the projected tag.
type Tag struct {
	ID            string
	Slug          string
	IsActive      bool
	NameAR        *string
	NameEN        *string
	Revision      int64
	SourceEventID string
}

// Edge is one projected parent→child DAG edge.
type Edge struct {
	ParentID string
	ChildID  string
}

// Repository is the catalog read boundary, implemented by the postgres
// adapter. Future adapters must not issue raw SQL.
type Repository interface {
	CatalogProduct(ctx context.Context, id string) (Product, error)
	CatalogActiveProducts(ctx context.Context, limit int) ([]ProductHeader, error)
	CatalogCategory(ctx context.Context, id string) (Category, error)
	CatalogCategoryEdges(ctx context.Context) ([]Edge, error)
	CatalogTag(ctx context.Context, id string) (Tag, error)
}

// Service fronts catalog reads for future phases.
type Service struct {
	repo Repository
}

// NewService wires the read service.
func NewService(repo Repository) Service { return Service{repo: repo} }

// GetProduct returns the full projected product or a NotFound error.
func (s Service) GetProduct(ctx context.Context, id string) (Product, error) {
	product, err := s.repo.CatalogProduct(ctx, id)
	if err != nil {
		return Product{}, err
	}
	return product, nil
}

// ListActiveProducts returns active product headers (no relations).
func (s Service) ListActiveProducts(ctx context.Context, limit int) ([]ProductHeader, error) {
	if limit <= 0 || limit > 500 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be 1..500")
	}
	return s.repo.CatalogActiveProducts(ctx, limit)
}

// GetCategory returns the projected category with its parent set.
func (s Service) GetCategory(ctx context.Context, id string) (Category, error) {
	return s.repo.CatalogCategory(ctx, id)
}

// CategoryDAG returns every projected parent→child edge.
func (s Service) CategoryDAG(ctx context.Context) ([]Edge, error) {
	return s.repo.CatalogCategoryEdges(ctx)
}

// TagsForProduct returns the projected tag IDs for one product.
func (s Service) TagsForProduct(ctx context.Context, productID string) ([]string, error) {
	product, err := s.repo.CatalogProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	return product.TagIDs, nil
}

// CategoriesForProduct returns the top category plus ordered subcategories.
func (s Service) CategoriesForProduct(ctx context.Context, productID string) (top string, subs []string, err error) {
	product, err := s.repo.CatalogProduct(ctx, productID)
	if err != nil {
		return "", nil, err
	}
	return product.TopCategoryID, product.SubcategoryIDs, nil
}

// GetTag returns the projected tag.
func (s Service) GetTag(ctx context.Context, id string) (Tag, error) {
	return s.repo.CatalogTag(ctx, id)
}
