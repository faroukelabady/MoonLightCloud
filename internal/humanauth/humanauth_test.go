package humanauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// RFC 4226 Appendix D: secret "12345678901234567890", counters 0..9.
func TestHOTPRFC4226Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		if got := HOTP(secret, uint64(counter)); got != code {
			t.Fatalf("HOTP(%d) = %s, want %s", counter, got, code)
		}
	}
}

// RFC 6238 Appendix B (SHA1), reduced from 8 to 6 digits (mod 10^6).
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, code := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471",
		1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := HOTP(secret, TOTPStep(time.Unix(unix, 0))); got != code {
			t.Fatalf("TOTP(%d) = %s, want %s", unix, got, code)
		}
	}
}

func TestVerifyTOTPWindowBoundaryAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_700_000_015, 0) // mid-step
	step := TOTPStep(now)
	code := func(s uint64) string { return HOTP(secret, s) }

	for name, tc := range map[string]struct {
		code    string
		at      time.Time
		last    uint64
		ok      bool
		matched uint64
	}{
		"current step":                             {code(step), now, 0, true, step},
		"previous step (skew window)":              {code(step - 1), now, 0, true, step - 1},
		"next step (future window)":                {code(step + 1), now, 0, true, step + 1},
		"two steps old (expired)":                  {code(step - 2), now, 0, false, 0},
		"two steps ahead":                          {code(step + 2), now, 0, false, 0},
		"replay of used step":                      {code(step), now, step, false, 0},
		"older than last used":                     {code(step - 1), now, step, false, 0},
		"boundary: last second of step accepts it": {code(step), time.Unix(int64(step*30+29), 0), 0, true, step},
		"boundary: next step still accepts prior":  {code(step), time.Unix(int64((step+1)*30), 0), 0, true, step},
		"wrong code":                               {"000000", now, 0, code(step) == "000000", 0},
		"too short":                                {"12345", now, 0, false, 0},
		"non digits":                               {"12a456", now, 0, false, 0},
		"spaces tolerated only":                    {code(step)[:3] + " " + code(step)[3:], now, 0, true, step},
	} {
		got, ok := VerifyTOTP(secret, tc.code, tc.at, tc.last)
		if ok != tc.ok || (ok && got != tc.matched) {
			t.Fatalf("%s: got (%d,%v) want (%d,%v)", name, got, ok, tc.matched, tc.ok)
		}
	}
}

func testBox(t *testing.T) *SecretBox {
	t.Helper()
	box, err := NewSecretBox(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestSecretBoxAuthenticatesAndBindsUser(t *testing.T) {
	box := testBox(t)
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("user-a", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("ciphertext must not contain the plaintext secret")
	}
	got, err := box.Open("user-a", sealed)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("roundtrip failed: %v", err)
	}
	if _, err := box.Open("user-b", sealed); !errors.Is(err, ErrSecretTampered) {
		t.Fatalf("copied to another user must fail: %v", err)
	}
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := box.Open("user-a", tampered); !errors.Is(err, ErrSecretTampered) {
		t.Fatalf("tampered ciphertext must fail: %v", err)
	}
	if _, err := box.Open("user-a", sealed[:5]); !errors.Is(err, ErrSecretTampered) {
		t.Fatalf("truncated ciphertext must fail: %v", err)
	}
	other, _ := NewSecretBox(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if _, err := other.Open("user-a", sealed); !errors.Is(err, ErrSecretTampered) {
		t.Fatalf("wrong key must fail: %v", err)
	}
	for _, bad := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewSecretBox(bad); !errors.Is(err, ErrMFAKey) {
			t.Fatalf("key %q must be rejected: %v", bad, err)
		}
	}
}

var cheap = Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestPasswordPolicyHashAndVerify(t *testing.T) {
	for name, tc := range map[string]struct {
		pw string
		ok bool
	}{
		"11 runes":              {"abcdefghijk", false},
		"12 runes":              {"abcdefghijkl", true},
		"12 Arabic runes":       {"كلمةمرورطويل", true},
		"256 runes":             {strings.Repeat("a", 256), true},
		"257 runes":             {strings.Repeat("a", 257), false},
		"over 1024 bytes":       {strings.Repeat("ك", 300), false},
		"invalid utf8":          {"abcdefghijk\xff", false},
		"whitespace only":       {strings.Repeat(" ", 20), false},
		"no composition needed": {"correct horse battery staple", true},
	} {
		if err := ValidatePassword(tc.pw); (err == nil) != tc.ok {
			t.Fatalf("%s: ValidatePassword=%v want ok=%v", name, err, tc.ok)
		}
	}
	hash, err := HashPassword("correct horse battery staple", cheap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("unexpected PHC: %s", hash)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("valid password rejected")
	}
	// No silent truncation: a prefix or extension never verifies.
	for _, wrong := range []string{"correct horse battery stapl", "correct horse battery staple!", "", strings.Repeat("x", 5000)} {
		if VerifyPassword(wrong, hash) {
			t.Fatalf("wrong password %q accepted", wrong)
		}
	}
	other, _ := HashPassword("correct horse battery staple", cheap)
	if other == hash {
		t.Fatal("salts must differ")
	}
}

func TestMalformedHashesFailClosedAndRehash(t *testing.T) {
	good, _ := HashPassword("correct horse battery staple", cheap)
	parts := strings.Split(good, "$")
	for name, phc := range map[string]string{
		"empty":             "",
		"bcrypt":            "$2a$10$abcdefghijklmnopqrstuuJ5lQ0Zl5bF6m5Z4l4Bn9v2xg8o4fW2",
		"wrong version":     strings.Replace(good, "v=19", "v=16", 1),
		"argon2i":           strings.Replace(good, "argon2id", "argon2i", 1),
		"huge memory":       "$argon2id$v=19$m=99999999,t=1,p=1$" + parts[4] + "$" + parts[5],
		"zero time":         "$argon2id$v=19$m=8192,t=0,p=1$" + parts[4] + "$" + parts[5],
		"bad params":        "$argon2id$v=19$m=8192,t=1$" + parts[4] + "$" + parts[5],
		"trailing params":   "$argon2id$v=19$m=8192,t=1,p=1,x=2$" + parts[4] + "$" + parts[5],
		"bad salt b64":      "$argon2id$v=19$m=8192,t=1,p=1$!!!$" + parts[5],
		"short key":         "$argon2id$v=19$m=8192,t=1,p=1$" + parts[4] + "$AAAA",
		"extra segment":     good + "$x",
		"sha256 hex digest": "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
	} {
		if VerifyPassword("correct horse battery staple", phc) {
			t.Fatalf("%s: malformed hash verified", name)
		}
		if !NeedsRehash(phc, CurrentArgon2) {
			t.Fatalf("%s: malformed hash must need rehash", name)
		}
	}
	if !NeedsRehash(good, CurrentArgon2) {
		t.Fatal("obsolete (weaker) parameters must be upgraded")
	}
	current, _ := HashPassword("correct horse battery staple", CurrentArgon2)
	if NeedsRehash(current, CurrentArgon2) {
		t.Fatal("current parameters must not rehash")
	}
}

func TestRecoveryCodesAndTokens(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount || len(hashes) != RecoveryCodeCount {
		t.Fatal("wrong count")
	}
	seen := map[string]bool{}
	for i, code := range codes {
		if len(code) != 19 || strings.Count(code, "-") != 3 || seen[code] {
			t.Fatalf("bad or duplicate code %q", code)
		}
		seen[code] = true
		loose := strings.ToLower(strings.ReplaceAll(code, "-", " "))
		if !bytes.Equal(HashRecoveryCode(loose), hashes[i]) {
			t.Fatal("normalization must make case/separators irrelevant")
		}
	}
	token, hash, err := NewToken()
	if err != nil || len(token) != 43 || !bytes.Equal(HashToken(token), hash) || len(hash) != 32 {
		t.Fatalf("token must be 256-bit base64url with a SHA-256 digest: %q %v", token, err)
	}
	other, _, _ := NewToken()
	if other == token {
		t.Fatal("tokens must be random")
	}
}

func TestRolePermissionsAndStoreAccess(t *testing.T) {
	for _, p := range []Permission{PermUsersManage, PermSecurityManage, PermUsersRead} {
		if !RoleHas(RoleOwner, p) || RoleHas(RoleAdmin, p) {
			t.Fatalf("%s must be OWNER-only", p)
		}
	}
	for _, p := range []Permission{PermCatalogManage, PermReportsRead, PermDevicesManage, PermReleasesManage, PermRolloutsManage, PermProvidersManage} {
		if !RoleHas(RoleOwner, p) || !RoleHas(RoleAdmin, p) {
			t.Fatalf("%s must be granted to OWNER and ADMIN", p)
		}
	}
	if RoleHas(Role("SUPERUSER"), PermCatalogRead) {
		t.Fatal("unknown role must have no permission")
	}
	restricted := Principal{Role: RoleAdmin, Stores: map[string]bool{"a": true}}
	if !restricted.CanAccessStore("a") || restricted.CanAccessStore("b") || restricted.CanAccessStore("") {
		t.Fatal("restricted access must be membership-only")
	}
	if !(Principal{AllStores: true}).CanAccessStore("anything") {
		t.Fatal("all-Stores principal must reach every Store")
	}
	for in, want := range map[string]string{"  Owner@Shop.Example ": "owner@shop.example", "ab": "", "has space": "", "-lead": ""} {
		got, err := NormalizeLogin(in)
		if (want == "") != (err != nil) || got != want {
			t.Fatalf("NormalizeLogin(%q) = %q, %v", in, got, err)
		}
	}
}
