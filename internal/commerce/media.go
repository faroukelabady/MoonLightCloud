package commerce

import (
	"context"
	"sort"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// ADR-0056 step 2: Product images on online channels. Media is an OPTIONAL
// provider capability (detected like the variant capability) that runs
// after product, variant and inventory convergence: images never delay
// stock, and a provider without the capability simply has no images.
//
// MoonLight only manages media it created. commerce_media_mappings names
// that media per (provider, Product, file hash); media added by hand in the
// shop is never reordered, attached or deleted.

// ProductImage is one desired gallery entry with its file state.
type ProductImage struct {
	ImageID     string
	VariantID   *string
	Position    int
	IsPrimary   bool
	SHA256      string
	ContentType string
	ByteSize    int
	Width       int
	Height      int
	// Available reports whether the bytes have reached Cloud.
	Available bool
}

// MediaCatalogReader is the OPTIONAL gallery read capability of a
// CatalogReader. catalog.Service satisfies it.
type MediaCatalogReader interface {
	GetProductMedia(ctx context.Context, productID string) ([]catalog.ProductMedia, error)
}

// MediaContent is one stored file.
type MediaContent struct {
	SHA256      string
	ContentType string
	Bytes       []byte
}

// MediaContentReader reads stored files by hash.
type MediaContentReader interface {
	MediaContent(ctx context.Context, sha256 string) (MediaContent, error)
}

// MediaMapping is one provider media item MoonLight created.
type MediaMapping struct {
	SHA256            string
	ExternalProductID string
	ExternalMediaID   string
}

// MediaMappingRepository is the OPTIONAL durable media mapping capability
// of a ProductMappingRepository. Delete is compare-and-delete on the
// external media identity.
type MediaMappingRepository interface {
	ListProductMediaMappings(ctx context.Context, providerKey ProviderKey, productID string) ([]MediaMapping, error)
	CreateProductMediaMapping(ctx context.Context, providerKey ProviderKey, productID string, mapping MediaMapping) error
	DeleteProductMediaMapping(ctx context.Context, providerKey ProviderKey, productID string, mapping MediaMapping) error
}

// MediaRecorder persists media identity while the adapter works: a file is
// recorded as soon as the provider created it, so a later failure can never
// leave MoonLight media the next run does not know about.
type MediaRecorder interface {
	Record(ctx context.Context, mapping MediaMapping) error
	Forget(ctx context.Context, mapping MediaMapping) error
}

// MediaImage is one file the provider product should show.
type MediaImage struct {
	SHA256      string
	ContentType string
	ByteSize    int
	Width       int
	Height      int
}

// MediaSyncRequest asks the adapter to converge the MoonLight-created media
// of one provider product. Images is the desired order (index 0 is the main
// image), one entry per distinct file. VariantImages names the main file of
// each Variant gallery (MoonLight Variant ID → hash) and ExistingVariants
// the mapped provider variations; both are empty for single-variant and
// framed products.
type MediaSyncRequest struct {
	ProviderKey       ProviderKey
	ProductID         string
	ExternalProductID string
	Alt               string
	Images            []MediaImage
	VariantImages     map[string]string
	ExistingVariants  map[string]string
	Mappings          []MediaMapping
	Content           MediaContentReader
	Recorder          MediaRecorder
}

// MediaSyncResult reports what the adapter did. Pending counts files the
// provider is still processing or that were not created yet (bounded work
// per run); the service retries later. Failed counts files the provider
// could not process; they are not retried until the image changes.
type MediaSyncResult struct {
	Created  int
	Attached int
	Deleted  int
	Pending  int
	Failed   int
}

// MediaCommerceProvider is the OPTIONAL media capability.
type MediaCommerceProvider interface {
	SyncProductMedia(ctx context.Context, req MediaSyncRequest) (MediaSyncResult, error)
}

func productImagesFrom(rows []catalog.ProductMedia) []ProductImage {
	images := make([]ProductImage, 0, len(rows))
	for _, row := range rows {
		images = append(images, ProductImage{
			ImageID: row.ImageID, VariantID: row.VariantID, Position: row.Position, IsPrimary: row.IsPrimary,
			SHA256: row.SHA256, ContentType: row.ContentType, ByteSize: row.ByteSize,
			Width: row.Width, Height: row.Height, Available: row.Available,
		})
	}
	return images
}

// galleryOrder sorts one gallery: the primary image first, then position.
func galleryOrder(entries []ProductImage) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsPrimary != entries[j].IsPrimary {
			return entries[i].IsPrimary
		}
		return entries[i].Position < entries[j].Position
	})
}

// desiredMedia orders the Product gallery first, then each Variant gallery
// in Variant order, keeps the first occurrence of each file and drops files
// that have not arrived. It also names each Variant gallery's main file
// when the provider shows one product per several variants.
func desiredMedia(product CommerceProduct, images []ProductImage) (ordered []MediaImage, variantMain map[string]string, missing int) {
	galleries := map[string][]ProductImage{}
	for _, image := range images {
		key := ""
		if image.VariantID != nil {
			key = *image.VariantID
		}
		galleries[key] = append(galleries[key], image)
	}
	keys := []string{""}
	for _, variant := range product.Variants {
		keys = append(keys, variant.VariantID)
	}
	seen, absent := map[string]bool{}, map[string]bool{}
	variantMain = map[string]string{}
	for _, key := range keys {
		gallery := galleries[key]
		galleryOrder(gallery)
		for _, image := range gallery {
			if !image.Available {
				if !absent[image.SHA256] {
					absent[image.SHA256] = true
					missing++
				}
				continue
			}
			if key != "" && len(product.Variants) > 1 && variantMain[key] == "" {
				variantMain[key] = image.SHA256
			}
			if seen[image.SHA256] {
				continue
			}
			seen[image.SHA256] = true
			ordered = append(ordered, MediaImage{
				SHA256: image.SHA256, ContentType: image.ContentType, ByteSize: image.ByteSize,
				Width: image.Width, Height: image.Height,
			})
		}
	}
	return ordered, variantMain, missing
}

// SetMediaContent wires the stored-file reader. Without it no images are
// sent to any provider.
func (s *CommerceService) SetMediaContent(reader MediaContentReader) {
	s.media = reader
}

// mediaRecorder persists provider media identity through the mapping seam.
type mediaRecorder struct {
	repo      MediaMappingRepository
	key       ProviderKey
	productID string
}

func (r mediaRecorder) Record(ctx context.Context, mapping MediaMapping) error {
	return r.repo.CreateProductMediaMapping(ctx, r.key, r.productID, mapping)
}

func (r mediaRecorder) Forget(ctx context.Context, mapping MediaMapping) error {
	return r.repo.DeleteProductMediaMapping(ctx, r.key, r.productID, mapping)
}

// syncMedia converges the provider product's MoonLight images. A product
// without images and without recorded media makes no provider call. Files
// still processing return a retryable error so the re-evaluation queue
// comes back for them.
func (s *CommerceService) syncMedia(ctx context.Context, provider CommerceProvider, key ProviderKey, productID string, desired DesiredProduct, externalProductID string, result *SyncResult) error {
	mediaProvider, ok := provider.(MediaCommerceProvider)
	if !ok || s.media == nil {
		return nil
	}
	repo, ok := s.mappings.(MediaMappingRepository)
	if !ok {
		return nil
	}
	mappings, err := repo.ListProductMediaMappings(ctx, key, productID)
	if err != nil {
		return err
	}
	images, variantMain, missing := desiredMedia(desired.Product, desired.Media)
	result.MediaMissing = missing
	if len(images) == 0 && len(mappings) == 0 {
		return nil
	}
	existing := map[string]string{}
	if len(variantMain) > 0 {
		rows, err := s.mappings.ListProductVariantMappings(ctx, key, productID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ExternalProductID == externalProductID {
				existing[row.VariantID] = row.ExternalVariantID
			}
		}
	}
	synced, err := mediaProvider.SyncProductMedia(ctx, MediaSyncRequest{
		ProviderKey: key, ProductID: productID, ExternalProductID: externalProductID,
		Alt: altText(desired.Product.Names), Images: images,
		VariantImages: variantMain, ExistingVariants: existing,
		Mappings: mappings, Content: s.media,
		Recorder: mediaRecorder{repo: repo, key: key, productID: productID},
	})
	if err != nil {
		return err
	}
	result.MediaCreated, result.MediaAttached, result.MediaDeleted = synced.Created, synced.Attached, synced.Deleted
	result.MediaPending, result.MediaFailed = synced.Pending, synced.Failed
	s.logInfo("commerce sync media", "provider", string(key), "product", productID,
		"created", synced.Created, "attached", synced.Attached, "deleted", synced.Deleted,
		"pending", synced.Pending, "failed", synced.Failed, "missing", missing)
	if synced.Pending > 0 {
		return TemporaryError("MEDIA_PENDING: provider media still processing")
	}
	return nil
}

// altText is the image description shoppers' screen readers hear: the
// Arabic name, English when Arabic is empty.
func altText(names []LocalizedName) string {
	fallback := ""
	for _, name := range names {
		switch name.Locale {
		case catalog.LocaleAR:
			if name.Name != "" {
				return name.Name
			}
		case catalog.LocaleEN:
			if fallback == "" {
				fallback = name.Name
			}
		}
	}
	return fallback
}

// loadMedia reads the Product's galleries when the reader supports it.
func loadMedia(ctx context.Context, reader CatalogReader, productID string) ([]ProductImage, error) {
	mediaReader, ok := reader.(MediaCatalogReader)
	if !ok {
		return nil, nil
	}
	rows, err := mediaReader.GetProductMedia(ctx, productID)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return productImagesFrom(rows), nil
}
