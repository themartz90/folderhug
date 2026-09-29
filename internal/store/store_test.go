package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestReopenPreservesDataAndDoesNotRepeatMigrations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO settings(key,value) VALUES('theme','dark')"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got string
	if err := s.DB.QueryRow("SELECT value FROM settings WHERE key='theme'").Scan(&got); err != nil || got != "dark" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReadersRemainAvailableDuringWrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DB.Exec("INSERT INTO settings(key,value) VALUES('visible','before')"); err != nil {
		t.Fatal(err)
	}
	writer, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.Exec("UPDATE settings SET value='after' WHERE key='visible'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	reader, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var value string
	if err = reader.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='visible'").Scan(&value); err != nil || value != "before" {
		t.Fatalf("read blocked or saw uncommitted data: %q %v", value, err)
	}
}

func TestConcurrentReadThenWriteTransactions(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DB.Exec("INSERT INTO settings(key,value) VALUES('counter','0')"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 4 {
		group.Go(func() {
			for range 10 {
				tx, err := s.DB.BeginTx(t.Context(), nil)
				if err != nil {
					t.Error(err)
					return
				}
				var value int
				if err = tx.QueryRow("SELECT value FROM settings WHERE key='counter'").Scan(&value); err == nil {
					_, err = tx.Exec("UPDATE settings SET value=? WHERE key='counter'", value+1)
				}
				if err != nil {
					tx.Rollback()
					t.Error(err)
					return
				}
				if err = tx.Commit(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	group.Wait()
	var value int
	if err = s.DB.QueryRow("SELECT value FROM settings WHERE key='counter'").Scan(&value); err != nil || value != 40 {
		t.Fatalf("lost update: %d %v", value, err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.migrate(context.Background(), fstest.MapFS{"migrations/999_broken.sql": {Data: []byte("CREATE TABLE should_rollback(id INTEGER); INVALID SQL;")}})
	if err == nil {
		t.Fatal("expected migration error")
	}
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='should_rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial migration remained: %d, %v", count, err)
	}
}

func TestUpgradeFromInitialScanSchemaPreservesPaths(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{
		"ALTER TABLE scan_jobs DROP COLUMN root_key",
		"ALTER TABLE scan_jobs DROP COLUMN active_since",
		"ALTER TABLE scan_index DROP COLUMN path_key",
		"DELETE FROM schema_migrations WHERE name='migrations/008_scan_paths.sql'",
		"INSERT INTO profiles(id,name,settings) VALUES('p','Synthetic','{}')",
		"INSERT INTO scan_jobs(id,profile_id,generation,creator_id,lease_id,owner,origin,directory_id,root_path,input,state,created_at,updated_at) VALUES('s','p',1,'owner','',1,'https://example.test','root','folder%20one/','{}','completed','2026-01-01','2026-01-01')",
		"INSERT INTO scan_index(profile_id,generation,entry_id,escaped_path,name_fold,path_fold,natural_key,category,metadata,last_scan_id) VALUES('p',1,'e','folder%20one/%2520.txt','file','file','file','other','{}','s')",
	}
	for _, command := range commands {
		if _, err = s.DB.Exec(command); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var root, path string
	if err = s.DB.QueryRow("SELECT root_key FROM scan_jobs WHERE id='s'").Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow("SELECT path_key FROM scan_index WHERE entry_id='e'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if root != "folder one/" || path != "folder one/%20.txt" {
		t.Fatalf("incorrect single decoding: %q %q", root, path)
	}
	var active *string
	if err = s.DB.QueryRow("SELECT active_since FROM scan_jobs WHERE id='s'").Scan(&active); err != nil || active != nil {
		t.Fatalf("active timestamp: %v %v", active, err)
	}
}
