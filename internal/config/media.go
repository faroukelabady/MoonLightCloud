package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// MediaConfig is Cloud-only. Product image bytes live in object storage
// (ADR-0057): a private R2 bucket, or a local directory in development only.
// PostgreSQL records which files arrived, never their bytes.
type MediaConfig struct {
	// Storage is "r2" or "local". Empty resolves to "local" in development
	// and is refused elsewhere.
	Storage         string
	Endpoint        string
	Bucket          string
	AccessKeyID     string `json:"-"`
	SecretAccessKey string `json:"-"`
	// LocalDir holds the files when Storage is "local".
	LocalDir string
}

func (c MediaConfig) String() string   { return "MediaConfig{credentials:redacted}" }
func (c MediaConfig) GoString() string { return c.String() }

var r2Host = regexp.MustCompile(`^[a-f0-9]{32}(\.(eu|us|fedramp))?\.r2\.cloudflarestorage\.com$`)
var r2Bucket = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// defaultLocalMediaDir is relative to the working directory (Git-ignored).
const defaultLocalMediaDir = ".media"

func loadMediaConfig() (MediaConfig, error) {
	c := MediaConfig{Storage: envOr("MEDIA_STORAGE", ""), LocalDir: envOr("MEDIA_LOCAL_DIR", defaultLocalMediaDir)}
	if c.Storage == "r2" {
		c.Endpoint = strings.TrimRight(strings.TrimSpace(os.Getenv("R2_ENDPOINT")), "/")
		c.Bucket = strings.TrimSpace(os.Getenv("R2_BUCKET"))
		c.AccessKeyID = strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID"))
		c.SecretAccessKey = strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY"))
	}
	return c, c.Validate()
}

// resolve applies the environment policy: deployed Clouds must use R2, and
// only development may keep images on the local disk.
func (c *MediaConfig) resolve(environment string) error {
	if c.Storage == "" {
		if environment != EnvDevelopment {
			return fmt.Errorf("MEDIA_STORAGE=r2 is required outside development")
		}
		c.Storage = "local"
		if c.LocalDir == "" {
			c.LocalDir = defaultLocalMediaDir
		}
	}
	if c.Storage == "local" && environment != EnvDevelopment {
		return fmt.Errorf("MEDIA_STORAGE=local is development only; use r2")
	}
	return c.Validate()
}

func (c MediaConfig) Validate() error {
	switch c.Storage {
	case "":
		return nil
	case "local":
		if strings.TrimSpace(c.LocalDir) == "" {
			return fmt.Errorf("MEDIA_LOCAL_DIR is required for MEDIA_STORAGE=local")
		}
		return nil
	case "r2":
	default:
		return fmt.Errorf("MEDIA_STORAGE must be r2 or local")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !r2Host.MatchString(u.Host) || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(c.Endpoint, "#") {
		return fmt.Errorf("R2_ENDPOINT must be an HTTPS Cloudflare R2 account endpoint without a path, port, userinfo, query or fragment")
	}
	if !r2Bucket.MatchString(c.Bucket) {
		return fmt.Errorf("R2_BUCKET must be a valid 3..63 character bucket name")
	}
	for _, secret := range []struct{ name, value string }{{"R2_ACCESS_KEY_ID", c.AccessKeyID}, {"R2_SECRET_ACCESS_KEY", c.SecretAccessKey}} {
		if secret.value == "" || len(secret.value) > 512 || strings.IndexFunc(secret.value, func(r rune) bool { return r <= 32 || r > 126 }) >= 0 {
			return fmt.Errorf("%s is required and must contain only printable non-space ASCII characters", secret.name)
		}
	}
	return nil
}
