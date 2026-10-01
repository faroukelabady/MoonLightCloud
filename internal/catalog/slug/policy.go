// Package slug holds the catalog tag slug wire policy shared by current
// catalog snapshots and immutable sale-time tag snapshots.
package slug

func Valid(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
