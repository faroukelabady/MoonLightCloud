// Package repositoryauth verifies TUF-authorised Sigstore release manifests.
// It owns network/cache I/O; the release package remains protocol-only.
package repositoryauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/release"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

const Format = release.RepositoryEnvelopeFormat
const Workflow = `^https://github\.com/faroukelabady/MoonLightRetail/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+)$`
const Repository = "https://github.com/faroukelabady/MoonLightRetail"
const Issuer = "https://token.actions.githubusercontent.com"

// Bootstrap is reviewed public configuration. Neither Cloud nor the envelope
// can supply it to Retail. Root and Sigstore material contain public keys only.
type Bootstrap struct {
	MetadataURL  string          `json:"metadata_url"`
	Root         json.RawMessage `json:"root"`
	SigstoreRoot json.RawMessage `json:"sigstore_trusted_root"`
}

type Envelope = release.RepositoryEnvelope

type Verifier struct {
	mu        sync.Mutex
	bootstrap Bootstrap
	cache     string
	offline   bool
	sigstore  *verify.Verifier
	// Tests substitute the transport, never cryptographic policy.
	client *http.Client
}

func DecodeBootstrap(raw []byte) (Bootstrap, error) {
	var b Bootstrap
	if err := strict(raw, &b); err != nil {
		return b, err
	}
	return b, nil
}

func New(b Bootstrap, cache string, offline bool) (*Verifier, error) {
	u, err := url.Parse(b.MetadataURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/") {
		return nil, errors.New("TUF metadata URL must be a public HTTPS directory")
	}
	if !filepath.IsAbs(cache) {
		return nil, errors.New("absolute private TUF cache directory required")
	}
	initial, err := trustedmetadata.New(b.Root)
	if err != nil {
		return nil, errors.New("invalid initial TUF root")
	}
	// Root signing authority must not overlap any routine online role.
	r := initial.Root.Signed
	for _, name := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		role := r.Roles[name]
		if role == nil || role.Threshold < 1 || len(role.KeyIDs) < role.Threshold {
			return nil, errors.New("missing or invalid TUF role")
		}
	}
	for _, role := range []string{metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		for _, a := range r.Roles[metadata.ROOT].KeyIDs {
			for _, c := range r.Roles[role].KeyIDs {
				if a == c {
					return nil, errors.New("offline root key must be separate from online role keys")
				}
			}
		}
	}
	material, err := root.NewTrustedRootFromJSON(b.SigstoreRoot)
	if err != nil {
		return nil, errors.New("invalid pinned Sigstore trust material")
	}
	sig, err := verify.NewVerifier(material, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return nil, err
	}
	return &Verifier{bootstrap: b, cache: cache, offline: offline, sigstore: sig, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Verify requires the same canonical manifest to be authorised by TUF and signed
// by the exact permitted workflow at the stated commit. There is no fallback.
func (v *Verifier) Verify(raw []byte) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var e Envelope
	if len(raw) > release.MaxEnvelopeBytes || strict(raw, &e) != nil || e.Format != Format {
		return nil, errors.New("invalid TUF/Sigstore envelope")
	}
	mraw, err := base64.StdEncoding.Strict().DecodeString(e.Manifest)
	if err != nil {
		return nil, err
	}
	m, err := release.ParseManifest(mraw)
	if err != nil {
		return nil, err
	}
	var b bundle.Bundle
	if err = b.UnmarshalJSON(e.Bundle); err != nil {
		return nil, errors.New("invalid Sigstore bundle")
	}
	if !b.HasInclusionProof() {
		return nil, errors.New("Sigstore inclusion proof required")
	}
	identity, err := certificateIdentity(m.BuildCommit)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(mraw)
	if _, err = v.sigstore.Verify(&b, verify.NewPolicy(verify.WithArtifactDigest("sha256", sum[:]), verify.WithCertificateIdentity(identity))); err != nil {
		return nil, errors.New("Sigstore workflow, commit, signature or log proof rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	up, err := v.refresh(ctx)
	if err != nil {
		return nil, errors.New("TUF refresh failed; release not authorised")
	}
	prefix := fmt.Sprintf("releases/%d/", m.ReleaseSequence)
	for name, data := range map[string][]byte{"manifest.json": mraw, "bundle.sigstore.json": e.Bundle} {
		target, err := up.GetTargetInfo(prefix + name)
		if err != nil || target.VerifyLengthHashes(data) != nil {
			return nil, errors.New("release bytes are not authorised TUF targets")
		}
	}
	return mraw, nil
}

func (v *Verifier) refresh(ctx context.Context) (*updater.Updater, error) {
	// The persisted root is itself a trust anchor after verified rotation.
	// Never read it through a shared/writable or symlinked cache directory.
	for _, dir := range []string{v.cache, filepath.Join(v.cache, "metadata"), filepath.Join(v.cache, "targets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("TUF cache must use real directories")
		}
		// Windows Retail inherits the ACL-checked journal directory. POSIX bits
		// cannot prove ACL privacy there; the host's journal boundary is required.
		if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("TUF cache must not be writable by other users")
		}
	}
	rootBytes := v.bootstrap.Root
	if raw, err := os.ReadFile(filepath.Join(v.cache, "metadata", "root.json")); err == nil {
		if len(raw) > 512000 {
			return nil, errors.New("cached root too large")
		}
		cached, err := metadata.Root().FromBytes(raw)
		if err != nil {
			return nil, err
		}
		initial, err := metadata.Root().FromBytes(v.bootstrap.Root)
		if err != nil {
			return nil, err
		}
		if cached.Signed.Version > initial.Signed.Version {
			rootBytes = raw
		}
		cachedCanonical, err := cached.ToBytes(false)
		if err != nil {
			return nil, err
		}
		initialCanonical, err := initial.ToBytes(false)
		if err != nil {
			return nil, err
		}
		if cached.Signed.Version == initial.Signed.Version && !bytes.Equal(cachedCanonical, initialCanonical) {
			return nil, errors.New("cached root conflicts with bootstrap")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	cfg, err := config.New(v.bootstrap.MetadataURL, rootBytes)
	if err != nil {
		return nil, err
	}
	cfg.LocalMetadataDir = filepath.Join(v.cache, "metadata")
	cfg.LocalTargetsDir = filepath.Join(v.cache, "targets")
	cfg.MaxRootRotations = 32
	cfg.MaxDelegations = 0
	cfg.Fetcher = &boundedFetcher{ctx: ctx, client: v.client, base: v.bootstrap.MetadataURL}
	// The helper validates the already refreshed, private cache without network.
	// go-tuf still checks signatures, hashes and expiry in this mode. It must never
	// be used for command intake, which requires the online TUF refresh workflow.
	cfg.UnsafeLocalMode = v.offline
	up, err := updater.New(cfg)
	if err != nil {
		return nil, err
	}
	if err = up.Refresh(); err != nil {
		return nil, err
	}
	return up, nil
}

type boundedFetcher struct {
	ctx    context.Context
	client *http.Client
	base   string
}

func (f *boundedFetcher) DownloadFile(location string, limit int64, _ time.Duration) ([]byte, error) {
	// A TUF server cannot redirect requests or choose a different origin/path.
	if !strings.HasPrefix(location, f.base) || strings.Contains(strings.TrimPrefix(location, f.base), "/") {
		return nil, errors.New("unexpected metadata URL")
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: resp.StatusCode, URL: location}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("metadata too large")
	}
	return data, nil
}

func strict(raw []byte, out any) error {
	if len(raw) == 0 {
		return errors.New("empty JSON")
	}
	// Reject duplicate member names at every nesting level before decoding.
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := unique(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func unique(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || seen[s] {
				return errors.New("duplicate JSON member")
			}
			seen[s] = true
			if err := unique(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := unique(d); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

func certificateIdentity(commit string) (verify.CertificateIdentity, error) {
	identity, err := verify.NewShortCertificateIdentity(Issuer, "", "", Workflow)
	identity.SourceRepositoryURI = Repository
	identity.SourceRepositoryDigest = commit
	identity.RunnerEnvironment = "github-hosted"
	return identity, err
}
