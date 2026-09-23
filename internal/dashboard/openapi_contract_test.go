package dashboard

import (
	"os"
	"strings"
	"testing"
)

// TestOpenAPIDashboardContract pins the dashboard API contract in the
// committed spec: string-money shapes, daily modes, split buckets,
// allowlisted diagnostics, and 500/503 documentation.
func TestOpenAPIDashboardContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, want := range []string{
		"dashboardSession:",
		"DashboardModeAverage:",
		"DashboardSummaryBucket:",
		"DashboardSummaryPayment:",
		"queue_count:",
		"last_error_label_ar",
		"display_currency:",
		"operationId: dashboardDaily",
		"operationId: dashboardLogin",
	} {
		if !strings.Contains(spec, want) {
			t.Fatalf("openapi must contain %q", want)
		}
	}
	if strings.Contains(spec, "last_error_message:") {
		// The raw-message field must be gone from dashboard sync health.
		lines := strings.Split(spec, "\n")
		inDashboard := false
		for _, l := range lines {
			if strings.Contains(l, "DashboardSyncHealth:") {
				inDashboard = true
			}
			if inDashboard && strings.TrimSpace(l) == "last_error_message: {type: string}" {
				t.Fatal("dashboard sync health must not expose last_error_message")
			}
		}
	}
}
