package shopify

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// ADR-0056 step 2 fake: Shopify's 2026-10 file model as far as product
// images go. Staged uploads land on stagedUploadPath of the harness host
// (the real target is another host; the access token must never reach
// it). fileCreate only accepts a resourceUrl whose bytes were uploaded,
// fileUpdate only attaches READY files, and deleting a file detaches it
// from its product and variants.

const stagedUploadPath = "/staged-upload/"

type fakeFile struct {
	gid      string
	filename string
	alt      string
	status   string
	content  []byte
}

type stagedRequest struct {
	token       string
	contentType string
	fields      map[string]string
	content     []byte
}

type fakeMediaStore struct {
	files map[string]*fakeFile // MediaImage GID -> file
	// product media order and variant media, by product and variant GID
	productMedia map[string][]string
	variantMedia map[string]string
	staged       map[string][]byte // resourceUrl -> uploaded bytes
	uploads      []stagedRequest
	stageCount   int
	// newStatus is the fileStatus of files created from now on.
	newStatus string
	// stagedScheme lets a test hand out a non-HTTPS upload target.
	stagedScheme string
}

func newFakeMediaStore() *fakeMediaStore {
	return &fakeMediaStore{
		files: map[string]*fakeFile{}, productMedia: map[string][]string{},
		variantMedia: map[string]string{}, staged: map[string][]byte{},
		newStatus: fileStatusReady, stagedScheme: "https",
	}
}

func (h *shopifyHarness) serveStagedUpload(w http.ResponseWriter, r *http.Request, raw []byte) {
	upload := stagedRequest{token: r.Header.Get("X-Shopify-Access-Token"), fields: map[string]string{}}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	sawFile := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		value, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			sawFile = true
			upload.content = value
			upload.contentType = part.Header.Get("Content-Type")
			continue
		}
		if sawFile {
			// Storage targets require every parameter before the file.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upload.fields[part.FormName()] = string(value)
	}
	h.media.uploads = append(h.media.uploads, upload)
	if !sawFile || upload.fields["key"] == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h.media.staged["https://test.myshopify.com"+stagedUploadPath+upload.fields["key"]] = upload.content
	w.WriteHeader(http.StatusNoContent)
}

func gqlUserError(root, message string) map[string]any {
	return map[string]any{"data": map[string]any{root: map[string]any{
		"userErrors": []any{map[string]any{"field": []any{"files"}, "message": message}},
	}}}
}

func (h *shopifyHarness) opMedia(operation string, vars map[string]any) map[string]any {
	m := h.media
	switch operation {
	case "MoonlightMediaProduct":
		id, err := ParseGID(str(vars["id"]), ResourceProduct)
		product := h.products[id]
		if err != nil || product == nil {
			return map[string]any{"data": map[string]any{"product": nil}}
		}
		media := []any{}
		for _, gid := range m.productMedia[product.gid] {
			status := fileStatusReady
			if file := m.files[gid]; file != nil {
				status = file.status
			}
			media = append(media, map[string]any{"id": gid, "status": status})
		}
		variants := []any{}
		for _, variant := range product.variants {
			nodes := []any{}
			if current := m.variantMedia[variant.gid]; current != "" {
				nodes = append(nodes, map[string]any{"id": current})
			}
			variants = append(variants, map[string]any{"id": variant.gid, "media": map[string]any{"nodes": nodes}})
		}
		return map[string]any{"data": map[string]any{"product": map[string]any{
			"id":         product.gid,
			"metafields": map[string]any{"nodes": metafieldNodes(product.metafields)},
			"media":      map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": media},
			"variants":   map[string]any{"nodes": variants},
		}}}
	case "MoonlightMediaFiles":
		nodes := []any{}
		for _, raw := range vars["ids"].([]any) {
			file := m.files[str(raw)]
			if file == nil {
				nodes = append(nodes, nil)
				continue
			}
			nodes = append(nodes, map[string]any{"id": file.gid, "fileStatus": file.status})
		}
		return map[string]any{"data": map[string]any{"nodes": nodes}}
	case "MoonlightMediaStage":
		targets := []any{}
		for range vars["input"].([]any) {
			m.stageCount++
			key := "tmp/" + strconv.Itoa(m.stageCount)
			targets = append(targets, map[string]any{
				"url":         m.stagedScheme + "://test.myshopify.com" + stagedUploadPath,
				"resourceUrl": "https://test.myshopify.com" + stagedUploadPath + key,
				"parameters": []any{
					map[string]any{"name": "key", "value": key},
					map[string]any{"name": "policy", "value": "signed-policy"},
				},
			})
		}
		return map[string]any{"data": map[string]any{"stagedUploadsCreate": map[string]any{
			"stagedTargets": targets, "userErrors": []any{},
		}}}
	case "MoonlightMediaCreate":
		files := []any{}
		for _, raw := range vars["files"].([]any) {
			input := raw.(map[string]any)
			content, ok := m.staged[str(input["originalSource"])]
			if !ok {
				return gqlUserError("fileCreate", "Image URL is invalid")
			}
			gid := FormatGID(ResourceMediaImage, h.allocateID())
			m.files[gid] = &fakeFile{gid: gid, filename: str(input["filename"]), alt: str(input["alt"]),
				status: m.newStatus, content: content}
			files = append(files, map[string]any{"id": gid, "fileStatus": "UPLOADED"})
		}
		return map[string]any{"data": map[string]any{"fileCreate": map[string]any{"files": files, "userErrors": []any{}}}}
	case "MoonlightMediaAttach":
		files := []any{}
		for _, raw := range vars["files"].([]any) {
			input := raw.(map[string]any)
			file := m.files[str(input["id"])]
			if file == nil || file.status != fileStatusReady {
				return gqlUserError("fileUpdate", "Non-ready files cannot be updated.")
			}
			for _, product := range input["referencesToAdd"].([]any) {
				m.productMedia[str(product)] = append(m.productMedia[str(product)], file.gid)
			}
			files = append(files, map[string]any{"id": file.gid})
		}
		return map[string]any{"data": map[string]any{"fileUpdate": map[string]any{"files": files, "userErrors": []any{}}}}
	case "MoonlightMediaDelete":
		deleted := []any{}
		for _, raw := range vars["fileIds"].([]any) {
			gid := str(raw)
			if m.files[gid] == nil {
				return gqlUserError("fileDelete", "File does not exist.")
			}
			delete(m.files, gid)
			for product, order := range m.productMedia {
				m.productMedia[product] = without(order, gid)
			}
			for variant, current := range m.variantMedia {
				if current == gid {
					delete(m.variantMedia, variant)
				}
			}
			deleted = append(deleted, gid)
		}
		return map[string]any{"data": map[string]any{"fileDelete": map[string]any{"deletedFileIds": deleted, "userErrors": []any{}}}}
	case "MoonlightMediaReorder":
		product := str(vars["id"])
		order := m.productMedia[product]
		for _, raw := range vars["moves"].([]any) {
			move := raw.(map[string]any)
			position, err := strconv.Atoi(str(move["newPosition"]))
			id := str(move["id"])
			if err != nil || !contains(order, id) {
				return gqlUserError("productReorderMedia", "Media does not exist")
			}
			order = without(order, id)
			position = min(position, len(order))
			order = append(order[:position], append([]string{id}, order[position:]...)...)
		}
		m.productMedia[product] = order
		return map[string]any{"data": map[string]any{"productReorderMedia": map[string]any{
			"job": map[string]any{"id": "gid://shopify/Job/1"}, "userErrors": []any{},
		}}}
	case "MoonlightMediaVariants":
		product := str(vars["productId"])
		updated := []any{}
		for _, raw := range vars["variants"].([]any) {
			input := raw.(map[string]any)
			if !contains(m.productMedia[product], str(input["mediaId"])) {
				return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
					"productVariants": nil, "userErrors": []any{map[string]any{"message": "Media is not on the product"}},
				}}}
			}
			m.variantMedia[str(input["id"])] = str(input["mediaId"])
			updated = append(updated, map[string]any{"id": str(input["id"])})
		}
		return map[string]any{"data": map[string]any{"productVariantsBulkUpdate": map[string]any{
			"productVariants": updated, "userErrors": []any{},
		}}}
	}
	return map[string]any{"errors": []any{map[string]any{"message": fmt.Sprintf("unknown media operation %s", operation)}}}
}

func without(values []string, drop string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// mediaOrder returns the product's media GIDs in display order.
func (h *shopifyHarness) mediaOrder(productGID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.media.productMedia[productGID]...)
}

// mediaMutations lists the media mutation names the adapter sent.
func (h *shopifyHarness) mediaMutations() []string {
	var names []string
	for _, request := range h.recorded() {
		if strings.HasPrefix(strings.TrimSpace(request.Query), "mutation MoonlightMedia") {
			names = append(names, operationName(request.Query))
		}
	}
	return names
}
