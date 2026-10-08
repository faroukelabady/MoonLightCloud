// Package release verifies Phase 18 signed release manifests (ADR-0051)
// for the Cloud release registry. It mirrors MoonLightRetail's verifier
// byte-for-byte in behavior (pinned by the shared cross-repository vector
// in testdata) without sharing code across the repository boundary. Cloud
// verification protects the registry; Retail always re-verifies
// independently against its own embedded keys. It is pure: no I/O.
//
// Trust model: an offline/CI release pipeline signs the exact canonical
// manifest bytes with an Ed25519 key. Retail embeds only public keys and
// verifies every release independently of Cloud. The signature covers the
// manifest bytes as transmitted; the manifest must additionally be in its
// single canonical encoding so one release has exactly one representation
// and one digest.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Format identifiers. Unknown values are rejected, never partially read.
const (
	EnvelopeFormat  = "moonlight-release-envelope-v1"
	ManifestVersion = 1
	Product         = "moonlightretail"
	// signingDomain separates release signatures from any other Ed25519
	// use of the same key material.
	signingDomain = "moonlight-release-manifest-v1\x00"
)

// Bounds. Large application bundles are streamed elsewhere; only the
// small signed metadata is ever held in memory.
const (
	MaxEnvelopeBytes = 64 * 1024
	MaxManifestBytes = 32 * 1024
	MaxArtifacts     = 16
	MaxArtifactBytes = int64(1) << 30
	// MaxSequence keeps sequences exact in JSON consumers (2^53).
	MaxSequence = uint64(1) << 53
)

// Package formats Retail understands. Installation support is decided by
// the platform adapter; the manifest only names the format.
const (
	PackageTarGz = "tar.gz"
)

// Code is a stable machine error code (prompt §72).
type Code string

const (
	CodeManifestInvalid     Code = "UPDATE_MANIFEST_INVALID"
	CodeManifestUnsupported Code = "UPDATE_MANIFEST_UNSUPPORTED"
	CodeKeyUnknown          Code = "UPDATE_KEY_UNKNOWN"
	CodeSignatureInvalid    Code = "UPDATE_SIGNATURE_INVALID"
	CodePlatformUnsupported Code = "UPDATE_PLATFORM_UNSUPPORTED"
	CodeDowngradeRejected   Code = "UPDATE_DOWNGRADE_REJECTED"
	CodeReleaseConflict     Code = "UPDATE_RELEASE_CONFLICT"
	CodeIncompatible        Code = "UPDATE_INSTALLED_INCOMPATIBLE"
	CodeHashMismatch        Code = "UPDATE_HASH_MISMATCH"
	CodeSizeMismatch        Code = "UPDATE_SIZE_MISMATCH"
)

// Error carries a stable code plus a bounded, non-sensitive detail.
type Error struct {
	Code   Code
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

func fail(code Code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// CodeOf extracts the stable code; "" when err is not a release error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Artifact is one platform build inside a release.
type Artifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Package  string `json:"package"`
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// Manifest is the signed release identity. Field order is the canonical
// encoding order (encoding/json emits struct fields in declaration order).
type Manifest struct {
	ManifestVersion      int        `json:"manifest_version"`
	Product              string     `json:"product"`
	ReleaseSequence      uint64     `json:"release_sequence"`
	Version              string     `json:"version"`
	BuildCommit          string     `json:"build_commit"`
	MinInstalledSequence uint64     `json:"min_installed_sequence"`
	KeyID                string     `json:"key_id"`
	Artifacts            []Artifact `json:"artifacts"`
}

// Envelope transports the exact signed manifest bytes.
type Envelope struct {
	Format    string `json:"format"`
	KeyID     string `json:"key_id"`
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
}

// Verified is a release whose signature, structure and canonical form
// have all been checked. Only Verify constructs it.
type Verified struct {
	Manifest      Manifest
	ManifestBytes []byte
	// Digest is lowercase hex SHA-256 of the exact signed manifest bytes:
	// the immutable release identity (prompt §144).
	Digest string
	KeyID  string
}

var (
	versionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	keyIDPattern    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	packagePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,31}$`)
)

var (
	knownOS   = map[string]bool{"linux": true, "windows": true, "darwin": true}
	knownArch = map[string]bool{"amd64": true, "arm64": true}
)

// KeyID derives the stable identifier of an Ed25519 public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// TrustSet is a small, immutable set of trusted public keys. It is built
// from keys embedded in the binary (or explicit development configuration)
// and can never be replaced by Cloud (prompt §5).
type TrustSet struct {
	keys map[string]ed25519.PublicKey
}

// NewTrustSet validates and indexes public keys by derived key ID.
func NewTrustSet(keys ...ed25519.PublicKey) (TrustSet, error) {
	set := TrustSet{keys: map[string]ed25519.PublicKey{}}
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			return TrustSet{}, errors.New("invalid ed25519 public key size")
		}
		set.keys[KeyID(key)] = append(ed25519.PublicKey(nil), key...)
	}
	return set, nil
}

// ParsePublicKey decodes a base64 (standard) Ed25519 public key.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(text))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("invalid ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

// Len reports the number of trusted keys.
func (t TrustSet) Len() int { return len(t.keys) }

// KeyIDs lists trusted key IDs in sorted order (diagnostics only).
func (t TrustSet) KeyIDs() []string {
	ids := make([]string, 0, len(t.keys))
	for id := range t.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SigningMessage is the exact byte string an Ed25519 signature covers.
func SigningMessage(manifestBytes []byte) []byte {
	return append([]byte(signingDomain), manifestBytes...)
}

// Canonical returns the single canonical encoding of m: struct field
// order, artifacts sorted by (os, arch, package), no insignificant
// whitespace, no HTML escaping.
func Canonical(m Manifest) ([]byte, error) {
	copied := m
	copied.Artifacts = append([]Artifact(nil), m.Artifacts...)
	sort.Slice(copied.Artifacts, func(i, j int) bool { return artifactLess(copied.Artifacts[i], copied.Artifacts[j]) })
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(copied); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func artifactLess(a, b Artifact) bool {
	if a.OS != b.OS {
		return a.OS < b.OS
	}
	if a.Arch != b.Arch {
		return a.Arch < b.Arch
	}
	return a.Package < b.Package
}

// Digest returns the lowercase hex SHA-256 of manifest bytes.
func Digest(manifestBytes []byte) string {
	sum := sha256.Sum256(manifestBytes)
	return hex.EncodeToString(sum[:])
}

// Verify performs every authenticity and structure check (prompt §8).
// Nothing about the release is trusted unless this returns nil error.
func (t TrustSet) Verify(envelopeBytes []byte) (Verified, error) {
	if len(envelopeBytes) == 0 || len(envelopeBytes) > MaxEnvelopeBytes {
		return Verified{}, fail(CodeManifestInvalid, "envelope size out of bounds")
	}
	var env Envelope
	if err := strictUnmarshal(envelopeBytes, &env); err != nil {
		return Verified{}, fail(CodeManifestInvalid, "envelope is not strict JSON")
	}
	if env.Format != EnvelopeFormat {
		return Verified{}, fail(CodeManifestUnsupported, "unsupported envelope format")
	}
	if !keyIDPattern.MatchString(env.KeyID) {
		return Verified{}, fail(CodeManifestInvalid, "malformed key id")
	}
	key, ok := t.keys[env.KeyID]
	if !ok {
		return Verified{}, fail(CodeKeyUnknown, "release signing key is not trusted")
	}
	manifestBytes, err := base64.StdEncoding.Strict().DecodeString(env.Manifest)
	if err != nil || len(manifestBytes) == 0 || len(manifestBytes) > MaxManifestBytes {
		return Verified{}, fail(CodeManifestInvalid, "manifest encoding invalid")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(env.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Verified{}, fail(CodeSignatureInvalid, "signature encoding invalid")
	}
	if !ed25519.Verify(key, SigningMessage(manifestBytes), signature) {
		return Verified{}, fail(CodeSignatureInvalid, "signature does not verify")
	}
	// Authentic bytes from here on; still parse strictly before use.
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return Verified{}, err
	}
	if manifest.KeyID != env.KeyID {
		return Verified{}, fail(CodeManifestInvalid, "manifest key id differs from envelope")
	}
	return Verified{Manifest: manifest, ManifestBytes: manifestBytes, Digest: Digest(manifestBytes), KeyID: env.KeyID}, nil
}

// ParseManifest strictly decodes and validates canonical manifest bytes.
// It does NOT check authenticity; callers must use TrustSet.Verify.
func ParseManifest(manifestBytes []byte) (Manifest, error) {
	if len(manifestBytes) == 0 || len(manifestBytes) > MaxManifestBytes {
		return Manifest{}, fail(CodeManifestInvalid, "manifest size out of bounds")
	}
	// Version gate first: a newer format is rejected safely rather than
	// partially interpreted (prompt §143).
	var probe struct {
		ManifestVersion json.RawMessage `json:"manifest_version"`
	}
	if err := json.Unmarshal(manifestBytes, &probe); err != nil {
		return Manifest{}, fail(CodeManifestInvalid, "manifest is not JSON")
	}
	if string(probe.ManifestVersion) != "1" {
		return Manifest{}, fail(CodeManifestUnsupported, "unsupported manifest version")
	}
	var m Manifest
	if err := strictUnmarshal(manifestBytes, &m); err != nil {
		return Manifest{}, fail(CodeManifestInvalid, "manifest is not strict JSON")
	}
	if err := validate(m); err != nil {
		return Manifest{}, err
	}
	canonical, err := Canonical(m)
	if err != nil || !bytes.Equal(canonical, manifestBytes) {
		return Manifest{}, fail(CodeManifestInvalid, "manifest is not in canonical form")
	}
	return m, nil
}

func validate(m Manifest) error {
	switch {
	case m.ManifestVersion != ManifestVersion:
		return fail(CodeManifestUnsupported, "unsupported manifest version")
	case m.Product != Product:
		return fail(CodeManifestInvalid, "unexpected product")
	case m.ReleaseSequence == 0 || m.ReleaseSequence > MaxSequence:
		return fail(CodeManifestInvalid, "release sequence out of range")
	case !versionPattern.MatchString(m.Version):
		return fail(CodeManifestInvalid, "malformed version")
	case !commitPattern.MatchString(m.BuildCommit):
		return fail(CodeManifestInvalid, "build commit must be a full lowercase SHA-1")
	case m.MinInstalledSequence >= m.ReleaseSequence:
		return fail(CodeManifestInvalid, "minimum installed sequence must precede the release")
	case !keyIDPattern.MatchString(m.KeyID):
		return fail(CodeManifestInvalid, "malformed key id")
	case len(m.Artifacts) == 0 || len(m.Artifacts) > MaxArtifacts:
		return fail(CodeManifestInvalid, "artifact count out of range")
	}
	for i, a := range m.Artifacts {
		switch {
		case !knownOS[a.OS]:
			return fail(CodeManifestInvalid, "artifact %d: unknown os", i)
		case !knownArch[a.Arch]:
			return fail(CodeManifestInvalid, "artifact %d: unknown arch", i)
		case !packagePattern.MatchString(a.Package):
			return fail(CodeManifestInvalid, "artifact %d: malformed package", i)
		case !fileNamePattern.MatchString(a.FileName):
			return fail(CodeManifestInvalid, "artifact %d: malformed file name", i)
		case a.Size <= 0 || a.Size > MaxArtifactBytes:
			return fail(CodeManifestInvalid, "artifact %d: size out of range", i)
		case !sha256Pattern.MatchString(a.SHA256):
			return fail(CodeManifestInvalid, "artifact %d: malformed sha256", i)
		}
		if i > 0 && !artifactLess(m.Artifacts[i-1], a) {
			return fail(CodeManifestInvalid, "artifacts not strictly ordered by platform")
		}
	}
	return nil
}

// SelectArtifact returns the artifact for exactly this platform and a
// package format the installer supports.
func SelectArtifact(m Manifest, goos, goarch string, supportedPackages ...string) (Artifact, error) {
	for _, a := range m.Artifacts {
		if a.OS != goos || a.Arch != goarch {
			continue
		}
		for _, p := range supportedPackages {
			if a.Package == p {
				return a, nil
			}
		}
	}
	return Artifact{}, fail(CodePlatformUnsupported, "no artifact for %s/%s", goos, goarch)
}

// Decision is the anti-replay/anti-downgrade verdict.
type Decision int

const (
	DecisionInstall Decision = iota + 1
	DecisionAlreadyInstalled
)

// CheckSequence applies the release-sequence policy (prompt §15, §132).
// installedSequence 0 means "no signed release recorded" (development or
// first managed install); installedDigest may be "" when unknown.
func CheckSequence(installedSequence uint64, installedDigest string, v Verified) (Decision, error) {
	seq := v.Manifest.ReleaseSequence
	switch {
	case seq < installedSequence:
		return 0, fail(CodeDowngradeRejected, "release %d precedes installed %d", seq, installedSequence)
	case seq == installedSequence:
		if installedDigest != "" && installedDigest != v.Digest {
			return 0, fail(CodeReleaseConflict, "same sequence with a different manifest digest")
		}
		return DecisionAlreadyInstalled, nil
	}
	if v.Manifest.MinInstalledSequence > installedSequence {
		return 0, fail(CodeIncompatible, "release requires installed sequence %d", v.Manifest.MinInstalledSequence)
	}
	return DecisionInstall, nil
}

// Sign produces an envelope. Release tooling only: private keys are never
// present in Retail or Cloud runtime (prompt §4).
func Sign(m Manifest, priv ed25519.PrivateKey) ([]byte, Verified, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, Verified{}, errors.New("invalid ed25519 private key")
	}
	pub, _ := priv.Public().(ed25519.PublicKey)
	m.KeyID = KeyID(pub)
	if m.ManifestVersion == 0 {
		m.ManifestVersion = ManifestVersion
	}
	if m.Product == "" {
		m.Product = Product
	}
	sort.Slice(m.Artifacts, func(i, j int) bool { return artifactLess(m.Artifacts[i], m.Artifacts[j]) })
	if err := validate(m); err != nil {
		return nil, Verified{}, err
	}
	manifestBytes, err := Canonical(m)
	if err != nil {
		return nil, Verified{}, err
	}
	signature := ed25519.Sign(priv, SigningMessage(manifestBytes))
	env := Envelope{
		Format:    EnvelopeFormat,
		KeyID:     m.KeyID,
		Manifest:  base64.StdEncoding.EncodeToString(manifestBytes),
		Signature: base64.StdEncoding.EncodeToString(signature),
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil, Verified{}, err
	}
	return out, Verified{Manifest: m, ManifestBytes: manifestBytes, Digest: Digest(manifestBytes), KeyID: m.KeyID}, nil
}

// strictUnmarshal decodes exactly one JSON object with no unknown fields,
// no duplicate keys and no trailing data.
func strictUnmarshal(data []byte, out any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := keyTok.(string)
				if seen[key] {
					return errors.New("duplicate key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk()
}
