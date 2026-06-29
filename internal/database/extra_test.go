package database

import (
	"testing"
	"time"
)

func TestUserCRUD(t *testing.T) {
	db := openTest(t)

	if n, _ := db.Users.Count(); n != 0 {
		t.Fatalf("expected 0 users initially, got %d", n)
	}

	u := User{ID: "u1", Username: "Admin", PasswordHash: "hash", Role: RoleAdmin}
	if err := db.Users.Create(u); err != nil {
		t.Fatal(err)
	}

	// Username uniqueness is case-insensitive on lookup.
	got, err := db.Users.GetByUsername("admin")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.Role != RoleAdmin || got.PasswordHash != "hash" {
		t.Errorf("unexpected user: %+v", got)
	}

	// Duplicate username rejected.
	if err := db.Users.Create(User{ID: "u2", Username: "admin", PasswordHash: "x", Role: RoleViewer}); err != ErrDuplicate {
		t.Errorf("expected ErrDuplicate, got %v", err)
	}

	// Update role + password.
	if err := db.Users.Update("u1", "admin2", RoleOperator, "newhash"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.Users.Get("u1")
	if got.Username != "admin2" || got.Role != RoleOperator || got.PasswordHash != "newhash" {
		t.Errorf("update not applied: %+v", got)
	}

	// Update without password keeps the old hash.
	if err := db.Users.Update("u1", "admin2", RoleViewer, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = db.Users.Get("u1")
	if got.PasswordHash != "newhash" || got.Role != RoleViewer {
		t.Errorf("password should be preserved: %+v", got)
	}

	if err := db.Users.Delete("u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Users.GetByUsername("admin2"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEventStore(t *testing.T) {
	db := openTest(t)
	if err := db.Cameras.Create(Camera{ID: "c", Name: "c", SourceType: SourceRTSP}); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Truncate(time.Millisecond)
	e := Event{ID: "e1", CameraID: "c", Type: EventMotion, StartTime: MS(base)}
	if err := db.Events.Create(e); err != nil {
		t.Fatal(err)
	}
	if err := db.Events.Finalize("e1", base.Add(5*time.Second), 0.42); err != nil {
		t.Fatal(err)
	}

	list, err := db.Events.List(EventFilter{CameraID: "c"})
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d err=%v", len(list), err)
	}
	if list[0].Score != 0.42 || list[0].EndTime.IsZero() {
		t.Errorf("finalize not applied: %+v", list[0])
	}

	// Time-window filter.
	if got, _ := db.Events.List(EventFilter{From: base.Add(time.Hour)}); len(got) != 0 {
		t.Errorf("expected no events after window, got %d", len(got))
	}

	// Cleanup by age.
	if n, _ := db.Events.DeleteOlderThan(base.Add(time.Hour)); n != 1 {
		t.Errorf("expected 1 event pruned, got %d", n)
	}

	// FK cascade on camera delete.
	db.Events.Create(Event{ID: "e2", CameraID: "c", StartTime: MS(base)})
	db.Cameras.Delete("c")
	if got, _ := db.Events.List(EventFilter{}); len(got) != 0 {
		t.Errorf("expected events cascade-deleted, got %d", len(got))
	}
}

func TestPushStore(t *testing.T) {
	db := openTest(t)
	sub := PushSubscription{ID: "p1", Endpoint: "https://push/1", P256dh: "k", Auth: "a"}
	if err := db.Push.Upsert(sub); err != nil {
		t.Fatal(err)
	}
	// Upsert on the same endpoint refreshes keys, not duplicates.
	sub.P256dh = "k2"
	if err := db.Push.Upsert(sub); err != nil {
		t.Fatal(err)
	}
	list, _ := db.Push.List()
	if len(list) != 1 || list[0].P256dh != "k2" {
		t.Fatalf("expected 1 refreshed sub, got %+v", list)
	}
	if err := db.Push.DeleteByEndpoint("https://push/1"); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.Push.List(); len(list) != 0 {
		t.Errorf("expected 0 subs after delete, got %d", len(list))
	}
}

func TestCameraMotionColumns(t *testing.T) {
	db := openTest(t)
	cam := Camera{ID: "c", Name: "c", SourceType: SourceRTSP, MotionEnabled: true, RecordMode: "motion", MotionSensitivity: 70}
	if err := db.Cameras.Create(cam); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Cameras.Get("c")
	if !got.MotionEnabled || got.RecordMode != "motion" || got.MotionSensitivity != 70 {
		t.Errorf("motion columns round-trip failed: %+v", got)
	}

	// Default record mode normalises to "continuous".
	db.Cameras.Create(Camera{ID: "d", Name: "d", SourceType: SourceRTSP})
	d, _ := db.Cameras.Get("d")
	if d.RecordMode != "continuous" {
		t.Errorf("expected default record mode continuous, got %q", d.RecordMode)
	}
}

func TestNotificationSettingsRoundTrip(t *testing.T) {
	db := openTest(t)
	if c, _ := db.Settings.Notifications(); !c.OnMotion {
		t.Error("expected default OnMotion true")
	}
	want := DefaultNotificationConfig()
	want.Enabled = true
	want.Email.Enabled = true
	want.Email.Host = "smtp.example.com"
	want.WebPush.PublicKey = "pub"
	want.WebPush.PrivateKey = "priv"
	if err := db.Settings.SetNotifications(want); err != nil {
		t.Fatal(err)
	}
	got, _ := db.Settings.Notifications()
	if !got.Enabled || got.Email.Host != "smtp.example.com" || got.WebPush.PrivateKey != "priv" {
		t.Errorf("notification round-trip failed: %+v", got)
	}
}
