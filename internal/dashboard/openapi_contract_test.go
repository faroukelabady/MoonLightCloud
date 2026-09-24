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
	// DashboardModeAverage must document every field the runtime emits:
	// ModeAverage carries transactions, units, and average_minor. An
	// undocumented emitted field is a contract defect (C3B-01 follow-up).
	lines := strings.Split(spec, "\n")
	inModeAvg := false
	seen := map[string]bool{}
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "DashboardModeAverage:") {
			inModeAvg = true
			continue
		}
		if inModeAvg {
			if trimmed == "" || (!strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t")) {
				break
			}
			for _, f := range []string{"transactions:", "units:", "average_minor:"} {
				if strings.HasPrefix(trimmed, f) {
					seen[f] = true
				}
			}
		}
	}
	for _, f := range []string{"transactions:", "units:", "average_minor:"} {
		if !seen[f] {
			t.Fatalf("DashboardModeAverage must document %q (runtime ModeAverage emits it)", f)
		}
	}
}
