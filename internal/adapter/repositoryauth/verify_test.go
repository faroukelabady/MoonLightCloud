package repositoryauth

import (
	"encoding/hex"
	"encoding/json"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/testing/data"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGitHubIdentityBindsRepositoryWorkflowCommitAndRunner(t *testing.T) {
	commit := strings.Repeat("a", 40)
	id, err := certificateIdentity(commit)
	require.NoError(t, err)
	good := certificate.Summary{SubjectAlternativeName: Repository + "/.github/workflows/release.yml@refs/heads/main", Issuer: Issuer}
	good.SourceRepositoryURI = Repository
	good.SourceRepositoryDigest = commit
	good.RunnerEnvironment = "github-hosted"
	require.NoError(t, id.Verify(good))
	cases := []certificate.Summary{good, good, good, good, good}
	cases[0].SourceRepositoryDigest = strings.Repeat("b", 40)
	cases[1].SourceRepositoryURI = "https://github.com/attacker/MoonLightRetail"
	cases[2].SubjectAlternativeName = Repository + "/.github/workflows/test.yml@refs/heads/main"
	cases[3].RunnerEnvironment = "self-hosted"
	cases[4].Issuer = "https://attacker.example"
	for _, bad := range cases {
		require.Error(t, id.Verify(bad))
	}
}
func TestRealSigstoreBundleCryptographicVerification(t *testing.T) {
	// Public upstream fixture: actual Fulcio chain/SCT, log proof, signature and
	// artifact binding. Its unrelated identity must fail MoonLight's policy.
	b := data.Bundle(t, "sigstore.js@2.0.0-provenance.sigstore.json")
	sev, err := verify.NewVerifier(data.TrustedRoot(t, "public-good.json"), verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	require.NoError(t, err)
	digest, err := hex.DecodeString("46d4e2f74c4877316640000a6fdf8a8b59f1e0847667973e9859f774dd31b8f1e0937813b777fb66a2ac67d50540fe34640966eee9fc2ccca387082b4c85cd3c")
	require.NoError(t, err)
	identity, err := verify.NewShortCertificateIdentity(Issuer, "", "", `^https://github\.com/sigstore/sigstore-js/\.github/workflows/release\.yml@refs/heads/main$`)
	require.NoError(t, err)
	_, err = sev.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha512", digest), verify.WithCertificateIdentity(identity)))
	require.NoError(t, err)
	digest[0] ^= 1
	_, err = sev.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha512", digest), verify.WithCertificateIdentity(identity)))
	require.Error(t, err)
	digest[0] ^= 1
	ours, err := certificateIdentity(strings.Repeat("a", 40))
	require.NoError(t, err)
	_, err = sev.Verify(b, verify.NewPolicy(verify.WithArtifactDigest("sha512", digest), verify.WithCertificateIdentity(ours)))
	require.Error(t, err)
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
}
