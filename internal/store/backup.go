package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// CopySQLite snapshots a Taaro database into a new file, including committed WAL data.
// Use it to back up a live database or restore a trusted backup to a fresh path.
func CopySQLite(ctx context.Context, source, destination string) error {
	if source == "" || destination == "" {
		return errors.New("database source and destination are required")
	}
	absoluteSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(absoluteSource)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("database source must be a regular file")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(destination + suffix); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return fmt.Errorf("destination or SQLite sidecar already exists: %s", destination+suffix)
		}
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	tempDir, err := os.MkdirTemp(parent, ".taaro-copy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	staged := filepath.Join(tempDir, "snapshot.db")
	file, err := os.OpenFile(staged, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	db, err := openReadOnlySQLite(absoluteSource)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", staged); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := validateSQLiteSnapshot(ctx, staged); err != nil {
		return err
	}
	snapshot, err := os.OpenFile(staged, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(snapshot.Sync(), snapshot.Close()); err != nil {
		return fmt.Errorf("sync database copy: %w", err)
	}
	if err := os.Link(staged, destination); err != nil {
		return fmt.Errorf("publish database copy: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("database copy was published, but its parent directory could not be synced: %w", err)
	}
	return nil
}

func openReadOnlySQLite(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	location := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func validateSQLiteSnapshot(ctx context.Context, path string) error {
	db, err := openReadOnlySQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("verify SQLite integrity: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", result)
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name IN ('webhook_deliveries', 'pull_requests', 'artifacts')").Scan(&tables); err != nil {
		return err
	}
	if tables != 3 {
		return errors.New("SQLite source is missing Taaro tables")
	}
	return nil
}
