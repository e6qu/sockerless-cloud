package gcp_sdk_test

import (
	"testing"

	admin "cloud.google.com/go/firestore/apiv1/admin"
	"cloud.google.com/go/firestore/apiv1/admin/adminpb"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
)

// newFSAdminClient connects the Firestore Admin REST client to the simulator,
// exactly as a real consumer would but with the sim's endpoint and no auth.
func newFSAdminClient(t *testing.T) *admin.FirestoreAdminClient {
	t.Helper()
	c, err := admin.NewFirestoreAdminRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	if err != nil {
		t.Fatalf("NewFirestoreAdminRESTClient: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// TestFirestoreAdmin_Databases exercises the database admin CRUD: CreateDatabase
// (a long-running operation), GetDatabase, and ListDatabases.
func TestFirestoreAdmin_Databases(t *testing.T) {
	c := newFSAdminClient(t)
	parent := "projects/fs-admin-db"

	op, err := c.CreateDatabase(ctx, &adminpb.CreateDatabaseRequest{
		Parent:     parent,
		DatabaseId: "(default)",
		Database: &adminpb.Database{
			LocationId: "nam5",
			Type:       adminpb.Database_FIRESTORE_NATIVE,
		},
	})
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	db, err := op.Wait(ctx)
	if err != nil {
		t.Fatalf("CreateDatabase.Wait: %v", err)
	}
	want := parent + "/databases/(default)"
	if db.GetName() != want {
		t.Fatalf("created database name = %q, want %q", db.GetName(), want)
	}

	got, err := c.GetDatabase(ctx, &adminpb.GetDatabaseRequest{Name: want})
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if got.GetName() != want {
		t.Fatalf("GetDatabase name = %q, want %q", got.GetName(), want)
	}

	list, err := c.ListDatabases(ctx, &adminpb.ListDatabasesRequest{Parent: parent})
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	found := false
	for _, d := range list.GetDatabases() {
		if d.GetName() == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListDatabases did not return %q", want)
	}
}

// TestFirestoreAdmin_Indexes exercises the index admin surface: CreateIndex (a
// long-running operation), GetIndex, ListIndexes, and DeleteIndex.
func TestFirestoreAdmin_Indexes(t *testing.T) {
	c := newFSAdminClient(t)
	cgParent := "projects/fs-admin-idx/databases/(default)/collectionGroups/users"

	op, err := c.CreateIndex(ctx, &adminpb.CreateIndexRequest{
		Parent: cgParent,
		Index: &adminpb.Index{
			QueryScope: adminpb.Index_COLLECTION,
			Fields: []*adminpb.Index_IndexField{
				{
					FieldPath: "name",
					ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	idx, err := op.Wait(ctx)
	if err != nil {
		t.Fatalf("CreateIndex.Wait: %v", err)
	}
	if idx.GetName() == "" {
		t.Fatalf("created index has empty name")
	}

	got, err := c.GetIndex(ctx, &adminpb.GetIndexRequest{Name: idx.GetName()})
	if err != nil {
		t.Fatalf("GetIndex: %v", err)
	}
	if got.GetName() != idx.GetName() {
		t.Fatalf("GetIndex name = %q, want %q", got.GetName(), idx.GetName())
	}

	it := c.ListIndexes(ctx, &adminpb.ListIndexesRequest{Parent: cgParent})
	found := false
	for {
		listed, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatalf("ListIndexes.Next: %v", err)
		}
		if listed.GetName() == idx.GetName() {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListIndexes did not return %q", idx.GetName())
	}

	if err := c.DeleteIndex(ctx, &adminpb.DeleteIndexRequest{Name: idx.GetName()}); err != nil {
		t.Fatalf("DeleteIndex: %v", err)
	}
	if _, err := c.GetIndex(ctx, &adminpb.GetIndexRequest{Name: idx.GetName()}); err == nil {
		t.Fatalf("GetIndex after delete: want error, got nil")
	}
}

// TestFirestoreAdmin_Fields exercises the field admin surface: UpdateField (a
// long-running operation), GetField, and ListFields.
func TestFirestoreAdmin_Fields(t *testing.T) {
	c := newFSAdminClient(t)
	fieldName := "projects/fs-admin-field/databases/(default)/collectionGroups/users/fields/email"

	op, err := c.UpdateField(ctx, &adminpb.UpdateFieldRequest{
		Field: &adminpb.Field{
			Name: fieldName,
			IndexConfig: &adminpb.Field_IndexConfig{
				Indexes: []*adminpb.Index{
					{QueryScope: adminpb.Index_COLLECTION},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateField: %v", err)
	}
	fld, err := op.Wait(ctx)
	if err != nil {
		t.Fatalf("UpdateField.Wait: %v", err)
	}
	if fld.GetName() != fieldName {
		t.Fatalf("UpdateField name = %q, want %q", fld.GetName(), fieldName)
	}

	got, err := c.GetField(ctx, &adminpb.GetFieldRequest{Name: fieldName})
	if err != nil {
		t.Fatalf("GetField: %v", err)
	}
	if got.GetName() != fieldName {
		t.Fatalf("GetField name = %q, want %q", got.GetName(), fieldName)
	}

	cgParent := "projects/fs-admin-field/databases/(default)/collectionGroups/users"
	it := c.ListFields(ctx, &adminpb.ListFieldsRequest{Parent: cgParent})
	found := false
	for {
		listed, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			t.Fatalf("ListFields.Next: %v", err)
		}
		if listed.GetName() == fieldName {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListFields did not return %q", fieldName)
	}
}

// TestFirestoreAdmin_BackupSchedules exercises the backup-schedule admin CRUD:
// CreateBackupSchedule, GetBackupSchedule, ListBackupSchedules,
// UpdateBackupSchedule, and DeleteBackupSchedule.
func TestFirestoreAdmin_BackupSchedules(t *testing.T) {
	c := newFSAdminClient(t)
	dbParent := "projects/fs-admin-bs/databases/(default)"

	created, err := c.CreateBackupSchedule(ctx, &adminpb.CreateBackupScheduleRequest{
		Parent: dbParent,
		BackupSchedule: &adminpb.BackupSchedule{
			Retention: durationpb.New(7 * 24 * 60 * 60 * 1e9),
			Recurrence: &adminpb.BackupSchedule_DailyRecurrence{
				DailyRecurrence: &adminpb.DailyRecurrence{},
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateBackupSchedule: %v", err)
	}
	if created.GetName() == "" {
		t.Fatalf("created backup schedule has empty name")
	}

	got, err := c.GetBackupSchedule(ctx, &adminpb.GetBackupScheduleRequest{Name: created.GetName()})
	if err != nil {
		t.Fatalf("GetBackupSchedule: %v", err)
	}
	if got.GetName() != created.GetName() {
		t.Fatalf("GetBackupSchedule name = %q, want %q", got.GetName(), created.GetName())
	}

	list, err := c.ListBackupSchedules(ctx, &adminpb.ListBackupSchedulesRequest{Parent: dbParent})
	if err != nil {
		t.Fatalf("ListBackupSchedules: %v", err)
	}
	found := false
	for _, bs := range list.GetBackupSchedules() {
		if bs.GetName() == created.GetName() {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListBackupSchedules did not return %q", created.GetName())
	}

	updated, err := c.UpdateBackupSchedule(ctx, &adminpb.UpdateBackupScheduleRequest{
		BackupSchedule: &adminpb.BackupSchedule{
			Name:      created.GetName(),
			Retention: durationpb.New(14 * 24 * 60 * 60 * 1e9),
			Recurrence: &adminpb.BackupSchedule_DailyRecurrence{
				DailyRecurrence: &adminpb.DailyRecurrence{},
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateBackupSchedule: %v", err)
	}
	if updated.GetName() != created.GetName() {
		t.Fatalf("UpdateBackupSchedule name = %q, want %q", updated.GetName(), created.GetName())
	}

	if err := c.DeleteBackupSchedule(ctx, &adminpb.DeleteBackupScheduleRequest{Name: created.GetName()}); err != nil {
		t.Fatalf("DeleteBackupSchedule: %v", err)
	}
	if _, err := c.GetBackupSchedule(ctx, &adminpb.GetBackupScheduleRequest{Name: created.GetName()}); err == nil {
		t.Fatalf("GetBackupSchedule after delete: want error, got nil")
	}
}
