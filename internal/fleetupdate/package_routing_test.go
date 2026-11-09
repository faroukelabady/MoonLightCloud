package fleetupdate

import "testing"

func TestPackageForOS(t *testing.T) {
	for _, tc := range []struct{ os, want string }{{"linux", "tar.gz"}, {"windows", "nsis-exe"}, {"darwin", ""}, {"", ""}} {
		if got := PackageForOS(tc.os); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.os, got, tc.want)
		}
	}
}
