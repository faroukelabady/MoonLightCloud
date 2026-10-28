package shopify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// ADR-0056 step 2: Product images on Shopify (Admin API 2026-10 file
// model). Each new file is uploaded through stagedUploadsCreate, created
// with fileCreate, recorded in commerce_media_mappings at once, and
// attached to the sellable product with fileUpdate once Shopify reports it
// READY. Only recorded media is ever reordered, set on a variant or
// deleted: media added by hand in Shopify keeps its place after
// MoonLight's images and is never touched.

const (
	// maxMediaCreatesPerSync bounds the uploads of one sync so a large
	// gallery never outlives the re-evaluation lease; the rest is pending.
	maxMediaCreatesPerSync = 10
	// mediaPollAttempts bounds the wait for freshly created files.
	mediaPollAttempts = 5
	// maxAltRunes is Shopify's image alt-text limit.
	maxAltRunes = 512
	// maxUploadResponseBytes bounds the staged-upload response body.
	maxUploadResponseBytes = 64 * 1024
)

// mediaPollDelay spaces the readiness checks of freshly created files.
var mediaPollDelay = time.Second

const (
	fileStatusReady  = "READY"
	fileStatusFailed = "FAILED"
)

// remoteMedia is the provider product as far as images are concerned.
type remoteMedia struct {
	order    []string          // product media GIDs in display order
	status   map[string]string // media GID -> status
	variants map[string]string // variant GID -> current media GID ("" none)
}

// SyncProductMedia implements commerce.MediaCommerceProvider.
func (p *ShopifyProvider) SyncProductMedia(ctx context.Context, req commerce.MediaSyncRequest) (commerce.MediaSyncResult, error) {
	if !commerce.ProductSyncHeld(ctx, p.key, req.ProductID) {
		if p.coordinator == nil {
			return commerce.MediaSyncResult{}, commerce.ValidationError("shopify product mutation requires database coordination")
		}
		var result commerce.MediaSyncResult
		err := p.coordinator.WithProductSync(ctx, p.key, req.ProductID, func(held context.Context) error {
			var err error
			result, err = p.SyncProductMedia(held, req)
			return err
		})
		return result, err
	}
	if req.ProviderKey != p.key {
		return commerce.MediaSyncResult{}, apperr.New(apperr.InvalidInput, "provider key mismatch")
	}
	if req.ProductID == "" || req.Content == nil || req.Recorder == nil {
		return commerce.MediaSyncResult{}, apperr.New(apperr.InvalidInput, "product id, content and recorder are required")
	}
	externalID, err := CanonicalDecimalID(req.ExternalProductID)
	if err != nil {
		return commerce.MediaSyncResult{}, commerce.ConflictError("mapped shopify product identity is malformed")
	}
	productGID := FormatGID(ResourceProduct, externalID)
	remote, err := p.loadMedia(ctx, productGID, req)
	if err != nil {
		return commerce.MediaSyncResult{}, err
	}

	var result commerce.MediaSyncResult
	wanted := map[string]bool{}
	for _, image := range req.Images {
		wanted[image.SHA256] = true
	}
	// Recorded media for this product and file, everything else is stale.
	mapped := map[string]commerce.MediaMapping{}
	var stale []commerce.MediaMapping
	for _, mapping := range req.Mappings {
		if wanted[mapping.SHA256] && mapping.ExternalProductID == externalID {
			mapped[mapping.SHA256] = mapping
			continue
		}
		stale = append(stale, mapping)
	}
	// Recorded files not on the product yet: are they still there?
	var probe []string
	for _, mapping := range req.Mappings {
		if _, attached := remote.status[mediaGID(mapping)]; !attached {
			probe = append(probe, mediaGID(mapping))
		}
	}
	files, err := p.fileStatuses(ctx, probe)
	if err != nil {
		return commerce.MediaSyncResult{}, err
	}

	var deletions []string
	for _, mapping := range stale {
		id := mediaGID(mapping)
		if _, attached := remote.status[id]; attached || files[id] != "" {
			deletions = append(deletions, id)
		}
	}
	if err := p.deleteFiles(ctx, deletions); err != nil {
		return commerce.MediaSyncResult{}, err
	}
	for _, mapping := range stale {
		if err := req.Recorder.Forget(ctx, mapping); err != nil {
			return commerce.MediaSyncResult{}, err
		}
	}
	result.Deleted = len(deletions)

	attachedIDs := map[string]string{} // sha -> media GID shown on the product
	var toAttach, creates []string
	for _, image := range req.Images {
		mapping, ok := mapped[image.SHA256]
		if !ok {
			creates = append(creates, image.SHA256)
			continue
		}
		id := mediaGID(mapping)
		if status, attached := remote.status[id]; attached {
			if status == fileStatusFailed {
				result.Failed++
				continue
			}
			attachedIDs[image.SHA256] = id
			continue
		}
		switch files[id] {
		case "":
			// Deleted in Shopify: forget it and upload again.
			if err := req.Recorder.Forget(ctx, mapping); err != nil {
				return commerce.MediaSyncResult{}, err
			}
			creates = append(creates, image.SHA256)
		case fileStatusReady:
			toAttach = append(toAttach, id)
			attachedIDs[image.SHA256] = id
		case fileStatusFailed:
			result.Failed++
		default:
			result.Pending++
		}
	}

	images := map[string]commerce.MediaImage{}
	for _, image := range req.Images {
		images[image.SHA256] = image
	}
	var created []string
	createdSHA := map[string]string{}
	for i, sha := range creates {
		if i >= maxMediaCreatesPerSync {
			result.Pending += len(creates) - i
			break
		}
		id, err := p.createFile(ctx, images[sha], req, externalID)
		if err != nil {
			return commerce.MediaSyncResult{}, err
		}
		if id == "" {
			result.Pending++
			continue
		}
		created = append(created, id)
		createdSHA[id] = sha
		result.Created++
	}
	if len(created) > 0 {
		statuses, err := p.awaitFiles(ctx, created)
		if err != nil {
			return commerce.MediaSyncResult{}, err
		}
		for _, id := range created {
			switch statuses[id] {
			case fileStatusReady:
				toAttach = append(toAttach, id)
				attachedIDs[createdSHA[id]] = id
			case fileStatusFailed:
				result.Failed++
			default:
				result.Pending++
			}
		}
	}
	if err := p.attachFiles(ctx, productGID, toAttach); err != nil {
		return commerce.MediaSyncResult{}, err
	}
	result.Attached = len(toAttach)

	current := append(append([]string{}, remote.order...), toAttach...)
	var mine []string
	for _, image := range req.Images {
		if id := attachedIDs[image.SHA256]; id != "" {
			mine = append(mine, id)
		}
	}
	if err := p.reorderMedia(ctx, productGID, current, mine); err != nil {
		return commerce.MediaSyncResult{}, err
	}
	if err := p.setVariantMedia(ctx, productGID, req, remote, attachedIDs); err != nil {
		return commerce.MediaSyncResult{}, err
	}
	return result, nil
}

// mediaGID rebuilds the Shopify identity of one recorded media item.
func mediaGID(mapping commerce.MediaMapping) string {
	return FormatGID(ResourceMediaImage, mapping.ExternalMediaID)
}

// loadMedia reads the product's media and variant images and proves that
// the product is the MoonLight product of this provider instance.
func (p *ShopifyProvider) loadMedia(ctx context.Context, productGID string, req commerce.MediaSyncRequest) (remoteMedia, error) {
	var out mediaProductResponse
	if err := p.client.do(ctx, docMediaProduct, map[string]any{"id": productGID}, &out); err != nil {
		return remoteMedia{}, err
	}
	if out.Product == nil {
		return remoteMedia{}, commerce.ConflictError("mapped shopify product no longer exists")
	}
	if responseID, err := CanonicalExternalID(out.Product.ID, ResourceProduct); err != nil || FormatGID(ResourceProduct, responseID) != productGID {
		return remoteMedia{}, commerce.ConflictError("shopify product identity mismatch")
	}
	if !ownershipMatches(ownership(out.Product.Metafields.Nodes), req.ProductID, req.ProviderKey) {
		return remoteMedia{}, commerce.ConflictError("shopify product owned by another product or provider")
	}
	if out.Product.Media.PageInfo.HasNextPage {
		return remoteMedia{}, commerce.ConflictError("shopify product has more media than MoonLight can order")
	}
	remote := remoteMedia{status: map[string]string{}, variants: map[string]string{}}
	for _, node := range out.Product.Media.Nodes {
		if node.ID == "" || remote.status[node.ID] != "" {
			return remoteMedia{}, commerce.ConflictError("shopify media identity ambiguous")
		}
		status := node.Status
		if status == "" {
			status = "UNKNOWN"
		}
		remote.order = append(remote.order, node.ID)
		remote.status[node.ID] = status
	}
	for _, variant := range out.Product.Variants.Nodes {
		current := ""
		if len(variant.Media.Nodes) > 0 {
			current = variant.Media.Nodes[0].ID
		}
		remote.variants[variant.ID] = current
	}
	return remote, nil
}

// fileStatuses reads the status of files that are not on the product.
// Missing files are absent from the result.
func (p *ShopifyProvider) fileStatuses(ctx context.Context, ids []string) (map[string]string, error) {
	statuses := map[string]string{}
	for start := 0; start < len(ids); start += 250 {
		end := min(start+250, len(ids))
		var out mediaFilesResponse
		if err := p.client.do(ctx, docMediaFiles, map[string]any{"ids": ids[start:end]}, &out); err != nil {
			return nil, err
		}
		for _, node := range out.Nodes {
			if node == nil || node.ID == "" {
				continue
			}
			status := node.FileStatus
			if status == "" {
				status = "UNKNOWN"
			}
			statuses[node.ID] = status
		}
	}
	return statuses, nil
}

// awaitFiles polls freshly created files until none is processing or the
// bounded wait ends.
func (p *ShopifyProvider) awaitFiles(ctx context.Context, ids []string) (map[string]string, error) {
	var statuses map[string]string
	for attempt := 0; attempt < mediaPollAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(mediaPollDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		var err error
		if statuses, err = p.fileStatuses(ctx, ids); err != nil {
			return nil, err
		}
		settled := true
		for _, id := range ids {
			if status := statuses[id]; status != fileStatusReady && status != fileStatusFailed {
				settled = false
			}
		}
		if settled {
			break
		}
	}
	return statuses, nil
}

// createFile uploads one stored file and creates it in Shopify. The new
// file is recorded before anything else can fail. An empty identity means
// the bytes are not stored after all; the image stays pending.
func (p *ShopifyProvider) createFile(ctx context.Context, image commerce.MediaImage, req commerce.MediaSyncRequest, externalProductID string) (string, error) {
	content, err := req.Content.MediaContent(ctx, image.SHA256)
	if err != nil {
		var appErr *apperr.Error
		if errors.As(err, &appErr) && appErr.Kind == apperr.NotFound {
			return "", nil
		}
		return "", err
	}
	digest := sha256.Sum256(content.Bytes)
	if hex.EncodeToString(digest[:]) != image.SHA256 || content.ContentType != image.ContentType {
		return "", commerce.ConflictError("stored media file does not match its hash")
	}
	filename := mediaFilename(image)
	var staged stagedUploadResponse
	if err := p.client.do(ctx, docMediaStage, map[string]any{"input": []map[string]any{{
		"resource": "IMAGE", "filename": filename, "mimeType": image.ContentType,
		"httpMethod": "POST", "fileSize": strconv.Itoa(len(content.Bytes)),
	}}}, &staged); err != nil {
		return "", err
	}
	if err := p.failUserErrors("stagedUploadsCreate", staged.StagedUploadsCreate.UserErrors); err != nil {
		return "", err
	}
	if len(staged.StagedUploadsCreate.StagedTargets) != 1 {
		return "", commerce.TemporaryError("shopify staged upload response missing target")
	}
	target := staged.StagedUploadsCreate.StagedTargets[0]
	if target.ResourceURL == "" {
		return "", commerce.TemporaryError("shopify staged upload response missing resource url")
	}
	if err := p.client.uploadStaged(ctx, target, filename, image.ContentType, content.Bytes); err != nil {
		return "", err
	}
	var out fileCreateResponse
	if err := p.client.do(ctx, docMediaCreate, map[string]any{"files": []map[string]any{{
		"originalSource": target.ResourceURL, "contentType": "IMAGE", "filename": filename,
		"alt": boundAlt(req.Alt), "duplicateResolutionMode": "APPEND_UUID",
	}}}, &out); err != nil {
		return "", err
	}
	if err := p.failUserErrors("fileCreate", out.FileCreate.UserErrors); err != nil {
		return "", err
	}
	if len(out.FileCreate.Files) != 1 {
		return "", commerce.TemporaryError("shopify file create response missing file")
	}
	decimal, err := CanonicalExternalID(out.FileCreate.Files[0].ID, ResourceMediaImage)
	if err != nil {
		return "", commerce.TemporaryError("shopify file create response identity malformed")
	}
	if err := req.Recorder.Record(ctx, commerce.MediaMapping{
		SHA256: image.SHA256, ExternalProductID: externalProductID, ExternalMediaID: decimal,
	}); err != nil {
		return "", err
	}
	return FormatGID(ResourceMediaImage, decimal), nil
}

// mediaFilename names a MoonLight file in the Shopify Files list.
func mediaFilename(image commerce.MediaImage) string {
	ext := ".jpg"
	if image.ContentType == "image/png" {
		ext = ".png"
	}
	return "moonlight-" + image.SHA256[:16] + ext
}

// boundAlt keeps alt text within Shopify's limit without splitting a rune.
func boundAlt(alt string) string {
	if utf8.RuneCountInString(alt) <= maxAltRunes {
		return alt
	}
	runes := []rune(alt)
	return string(runes[:maxAltRunes])
}

// attachFiles adds READY files to the product.
func (p *ShopifyProvider) attachFiles(ctx context.Context, productGID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	inputs := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		inputs = append(inputs, map[string]any{"id": id, "referencesToAdd": []string{productGID}})
	}
	var out fileUpdateResponse
	if err := p.client.do(ctx, docMediaAttach, map[string]any{"files": inputs}, &out); err != nil {
		return err
	}
	return p.failUserErrors("fileUpdate", out.FileUpdate.UserErrors)
}

// deleteFiles deletes MoonLight files that no gallery shows any more.
func (p *ShopifyProvider) deleteFiles(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var out fileDeleteResponse
	if err := p.client.do(ctx, docMediaDelete, map[string]any{"fileIds": ids}, &out); err != nil {
		return err
	}
	return p.failUserErrors("fileDelete", out.FileDelete.UserErrors)
}

// reorderMedia puts MoonLight's images first, in gallery order (position 0
// is the product's main image). Other media keeps its relative order after
// them. Nothing is sent when the order already holds.
func (p *ShopifyProvider) reorderMedia(ctx context.Context, productGID string, current, mine []string) error {
	inOrder := len(current) >= len(mine)
	for i := 0; inOrder && i < len(mine); i++ {
		inOrder = current[i] == mine[i]
	}
	if inOrder {
		return nil
	}
	moves := make([]map[string]any, 0, len(mine))
	for i, id := range mine {
		moves = append(moves, map[string]any{"id": id, "newPosition": strconv.Itoa(i)})
	}
	var out reorderMediaResponse
	if err := p.client.do(ctx, docMediaReorder, map[string]any{"id": productGID, "moves": moves}, &out); err != nil {
		return err
	}
	return p.failUserErrors("productReorderMedia", out.ProductReorderMedia.UserErrors)
}

// setVariantMedia shows each Variant gallery's main image on its mapped
// Shopify variant. Variants that are not on this product are skipped.
func (p *ShopifyProvider) setVariantMedia(ctx context.Context, productGID string, req commerce.MediaSyncRequest, remote remoteMedia, attached map[string]string) error {
	var updates []map[string]any
	for variantID, sha := range req.VariantImages {
		external := req.ExistingVariants[variantID]
		mediaID := attached[sha]
		if external == "" || mediaID == "" {
			continue
		}
		variantGID := FormatGID(ResourceProductVariant, external)
		current, ok := remote.variants[variantGID]
		if !ok || current == mediaID {
			continue
		}
		updates = append(updates, map[string]any{"id": variantGID, "mediaId": mediaID})
	}
	if len(updates) == 0 {
		return nil
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i]["id"].(string) < updates[j]["id"].(string) })
	var out variantMediaResponse
	if err := p.client.do(ctx, docMediaVariants, map[string]any{"productId": productGID, "variants": updates}, &out); err != nil {
		return err
	}
	return p.failUserErrors("productVariantsBulkUpdate", out.ProductVariantsBulkUpdate.UserErrors)
}

// uploadStaged posts one file to the staged-upload target as a multipart
// form: the target's parameters first, the file last. The Shopify access
// token is never sent; only HTTPS targets are accepted and redirects are
// refused like every other request.
func (c *Client) uploadStaged(ctx context.Context, target stagedTarget, filename, contentType string, content []byte) error {
	if err := commerce.ProductSyncGuard(ctx); err != nil {
		return err
	}
	parsed, err := url.Parse(target.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return commerce.ValidationError("shopify staged upload target must be https")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, parameter := range target.Parameters {
		if err := form.WriteField(parameter.Name, parameter.Value); err != nil {
			return err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", contentType)
	part, err := form.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("User-Agent", c.userAgent)
	response, err := c.upload.Do(request)
	if err != nil {
		return classifyTransport(ctx, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxUploadResponseBytes))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return commerce.TemporaryError(fmt.Sprintf("shopify staged upload refused with status %d", response.StatusCode))
	}
	return nil
}

var _ commerce.MediaCommerceProvider = (*ShopifyProvider)(nil)
