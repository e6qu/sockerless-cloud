package main

import "testing"

func TestSpannerTranslateDDLRewritesSizedTypes(t *testing.T) {
	got, ok := spannerTranslateDDL(
		"CREATE TABLE Users (UserId STRING(36) NOT NULL, DisplayName STRING(MAX), Payload BYTES(MAX)) PRIMARY KEY (UserId)",
	)
	if !ok {
		t.Fatal("CREATE TABLE was not recognized")
	}
	want := `CREATE TABLE "Users" (UserId TEXT NOT NULL, DisplayName TEXT, Payload BLOB, PRIMARY KEY (UserId))`
	if got != want {
		t.Fatalf("translated DDL = %q, want %q", got, want)
	}

	got, ok = spannerTranslateDDL("ALTER TABLE Users ADD COLUMN Email STRING(MAX)")
	if !ok || got != "ALTER TABLE Users ADD COLUMN Email TEXT" {
		t.Fatalf("translated ALTER TABLE = %q, %v", got, ok)
	}
}
