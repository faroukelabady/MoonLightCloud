package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ADR-0055 step 2e: both recipient features share notification_recipients
// and stay separated by purpose; a schedule only accepts business-report
// recipients.
func TestNotificationRecipientPurposeIsolation(t *testing.T) {
	pool, _ := openTestRepo(t)
	ctx := context.Background()
	d := NewDevices(pool, 5*time.Second)
	reportID, alertID := uuid.NewString(), uuid.NewString()
	if err := NewBusinessReports(d.pool, d.timeout).CreateRecipient(ctx, reportID, "report", "telegram-main", "@report", "en", true); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOperations(d.pool, d.timeout).CreateOpsRecipient(ctx, alertID, "alert", "telegram-main", "@alert", "en", time.Now()); err != nil {
		t.Fatal(err)
	}
	alerts, err := NewOperations(d.pool, d.timeout).ListOpsRecipients(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].ID != alertID {
		t.Fatalf("alert recipients: %+v %v", alerts, err)
	}
	if _, found, err := NewBusinessReports(d.pool, d.timeout).GetRecipient(ctx, alertID); err != nil || found {
		t.Fatalf("an alert recipient must not read as a business-report recipient: found=%v %v", found, err)
	}

	schedule := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO business_report_schedules
		(id, name, report_kind, timezone, local_time, next_run_local_date, next_run_at)
		VALUES ($1, 'daily', 'daily', 'Africa/Cairo', '08:00', current_date, now())`, schedule); err != nil {
		t.Fatal(err)
	}
	link := `INSERT INTO business_report_schedule_recipients (schedule_id, recipient_id) VALUES ($1, $2)`
	if _, err := pool.Exec(ctx, link, schedule, reportID); err != nil {
		t.Fatalf("business-report recipient: %v", err)
	}
	if _, err := pool.Exec(ctx, link, schedule, alertID); err == nil {
		t.Fatal("an alert recipient must not join a report schedule")
	}
}

// ADR-0055 step 2e: capabilities are rows; announcing false removes it.
func TestDeviceCapabilityRows(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "capability-dev")
	if err != nil {
		t.Fatal(err)
	}
	d := NewCatalogAdmin(pool, 5*time.Second)
	read := func() bool {
		t.Helper()
		capable, err := d.GetCatalogAdminCapability(ctx, p.Device.ID)
		if err != nil {
			t.Fatal(err)
		}
		return capable
	}
	if read() {
		t.Fatal("no announcement means incapable")
	}
	for i := 0; i < 2; i++ {
		if err := d.ReplaceCatalogAdminCapabilities(ctx, p.Device.ID, []string{catalogadmin.CapabilityV1}); err != nil {
			t.Fatal(err)
		}
	}
	if !read() {
		t.Fatal("announced capability must read back")
	}
	if err := d.ReplaceCatalogAdminCapabilities(ctx, p.Device.ID, nil); err != nil {
		t.Fatal(err)
	}
	if read() {
		t.Fatal("withdrawn capability must be gone")
	}
}

// ADR-0055 step 2e: presence has no last_seen_at of its own; control
// contact advances devices.last_seen_at and never moves it backwards.
func TestControlContactAdvancesDeviceLastSeen(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "presence-dev")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDevices(pool, 5*time.Second)
	later := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	if err := NewDeviceControl(d.pool, d.timeout).TouchSeen(ctx, p.Device.ID, later, true); err != nil {
		t.Fatal(err)
	}
	if err := NewDeviceControl(d.pool, d.timeout).TouchAccepted(ctx, p.Device.ID, later.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	presence, found, err := NewDeviceControl(d.pool, d.timeout).GetPresence(ctx, p.Device.ID)
	if err != nil || !found || presence.LastSeenAt == nil || !presence.LastSeenAt.Equal(later) {
		t.Fatalf("presence last seen: %+v found=%v %v", presence, found, err)
	}
}

// Presence reports the newest sync event a device uploaded, so the
// dashboard can show data freshness next to control-channel state.
func TestPresenceReportsLastDataAt(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "data-dev")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDevices(pool, 5*time.Second)
	storeID := bindTestStore(t, d, p.Device.ID)
	if err := NewDeviceControl(d.pool, d.timeout).TouchSeen(ctx, p.Device.ID, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	presence, _, err := NewDeviceControl(d.pool, d.timeout).GetPresence(ctx, p.Device.ID)
	if err != nil || presence.LastDataAt != nil || presence.LastPollAt != nil {
		t.Fatalf("no data or poll yet: %+v %v", presence, err)
	}
	received := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash, store_id)
		VALUES ($1, $2, 'sale.finalized.v3', now(), $3, '{}', '\x00', $4)`, uuid.NewString(), p.Device.ID, received, storeID); err != nil {
		t.Fatal(err)
	}
	presence, _, err = NewDeviceControl(d.pool, d.timeout).GetPresence(ctx, p.Device.ID)
	if err != nil || presence.LastDataAt == nil || !presence.LastDataAt.Equal(received) {
		t.Fatalf("last data: %+v %v", presence, err)
	}
}

// The Store allows a scope when any active, bound, command-capable device
// advertises it; scopes without the command channel count for nothing.
func TestStoreCatalogAdminPermissions(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	d := NewCatalogAdmin(pool, 5*time.Second)
	devices := NewDevices(pool, 5*time.Second)
	first, err := authSvc.Create(ctx, "permissions-dev-1")
	if err != nil {
		t.Fatal(err)
	}
	storeID := bindTestStore(t, devices, first.Device.ID)
	read := func() catalogadmin.StorePermissions {
		t.Helper()
		p, err := d.CatalogAdminStorePermissions(ctx, storeID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := read(); got != (catalogadmin.StorePermissions{}) {
		t.Fatalf("no announcement allows nothing: %+v", got)
	}
	if err := d.ReplaceCatalogAdminCapabilities(ctx, first.Device.ID, []string{catalogadmin.CapabilityV1, catalogadmin.CapabilityScopeOnlineSelling}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (catalogadmin.StorePermissions{OnlineSelling: true, Devices: 1}) {
		t.Fatalf("shop defaults: %+v", got)
	}
	if err := d.ReplaceCatalogAdminCapabilities(ctx, first.Device.ID, []string{catalogadmin.CapabilityV1, catalogadmin.CapabilityScopeProducts, catalogadmin.CapabilityScopeCatalog}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (catalogadmin.StorePermissions{Products: true, Catalog: true, Devices: 1}) {
		t.Fatalf("a new announcement replaces the old one: %+v", got)
	}
	if err := d.ReplaceCatalogAdminCapabilities(ctx, first.Device.ID, []string{catalogadmin.CapabilityScopeProducts}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (catalogadmin.StorePermissions{}) {
		t.Fatalf("scopes without the command channel allow nothing: %+v", got)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_capabilities (device_id, capability) VALUES ($1, 'catalog_admin_scope_everything_v1')`, first.Device.ID); err == nil {
		t.Fatal("unknown capability names must be refused by the schema")
	}
}

// A real authenticated sync client can contact Cloud before it ever polls
// control. Presence must read canonical device contact without inventing a poll.
func TestPresenceIncludesAuthenticatedDeviceWithoutControlRow(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "upload-only")
	if err != nil {
		t.Fatal(err)
	}
	ctl := NewDeviceControl(pool, 5*time.Second)
	if _, found, err := ctl.GetPresence(ctx, p.Device.ID); err != nil || found {
		t.Fatalf("unseen device: found=%v %v", found, err)
	}
	if _, _, err := authSvc.Authenticate(ctx, p.Device.ID, p.Credential.ID, "invalid"); err == nil {
		t.Fatal("invalid auth accepted")
	}
	if _, found, err := ctl.GetPresence(ctx, p.Device.ID); err != nil || found {
		t.Fatalf("invalid auth created presence: %v %v", found, err)
	}
	dev, _, err := authSvc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret)
	if err != nil {
		t.Fatal(err)
	}
	storeID := bindTestStore(t, NewDevices(pool, 5*time.Second), p.Device.ID)
	received := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash, store_id)
 VALUES ($1, $2, 'sale.finalized.v3', now(), $3, '{}', '\x00', $4)`, uuid.NewString(), p.Device.ID, received, storeID); err != nil {
		t.Fatal(err)
	}
	check := func(presence devicecontrol.Presence) {
		t.Helper()
		if presence.LastSeenAt == nil || dev.LastSeenAt == nil || presence.LastSeenAt.Sub(*dev.LastSeenAt).Abs() > time.Microsecond || presence.LastPollAt != nil || presence.LastDataAt == nil || !presence.LastDataAt.Equal(received) {
			t.Fatalf("authenticated-only presence: %+v", presence)
		}
	}
	presence, found, err := ctl.GetPresence(ctx, p.Device.ID)
	if err != nil || !found {
		t.Fatalf("presence missing: %v %v", found, err)
	}
	check(presence)
	all, err := ctl.ListPresence(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("batched presence: %+v %v", all, err)
	}
	check(all[0])
	var controls int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_control_presence WHERE device_id=$1`, p.Device.ID).Scan(&controls); err != nil || controls != 0 {
		t.Fatalf("read fabricated a control row: %d %v", controls, err)
	}
}
