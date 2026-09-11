package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/file"
	"github.com/stretchr/testify/require"
)

// 公司版已执行的迁移编号必须保留；官方新迁移顺延，避免重复或漏执行。
func TestCompanyMigrationSequencesAreUnambiguous(t *testing.T) {
	for _, spec := range []struct {
		name string
		last uint
	}{{"versioned", 96}, {"sqlite", 15}} {
		t.Run(spec.name, func(t *testing.T) {
			path := filepath.Join(sqliteRepoRoot(t), "migrations", spec.name)
			driver, err := (&file.File{}).Open("file://" + path)
			require.NoError(t, err)
			defer driver.Close()
			version, err := driver.First()
			require.NoError(t, err)
			require.Zero(t, version)
			for expected := uint(1); expected <= spec.last; expected++ {
				version, err = driver.Next(version)
				require.NoError(t, err)
				require.Equal(t, expected, version)
			}
			_, err = driver.Next(version)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestSQLiteCompanyV5UpgradePreservesFoldersAndUploads(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	legacyRoot := copySQLiteMigrationsV4(t, repoRoot)
	name := "000005_knowledge_folders_and_uploads.up.sql"
	data, err := os.ReadFile(filepath.Join(repoRoot, "migrations", "sqlite", name))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(legacyRoot, "migrations", "sqlite", name), data, 0o600))
	chdirAndRestore(t, legacyRoot)
	dbPath := filepath.Join(t.TempDir(), "company-v5.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db := openSQLiteDB(t, dbPath)
	_, err = db.Exec("INSERT INTO knowledge_folders (id, tenant_id, knowledge_base_id, path) VALUES ('company-folder', 1, 'company-kb', '公司/空目录')")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO knowledge_upload_sessions
		(id, tenant_id, knowledge_base_id, user_id, file_name, file_size, chunk_size,
		received_bytes, status, temp_path, expires_at)
		VALUES ('company-upload', 1, 'company-kb', 'company-user', '续传.pdf', 8388608,
		4194304, 4194304, 'uploading', '/data/files/upload-sessions/company-upload', '2099-01-01')`)
	require.NoError(t, err)
	chdirAndRestore(t, repoRoot)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	version, dirty := sqliteMigrationState(t, db)
	require.Equal(t, expectedSQLiteMigrationVersion, version)
	require.False(t, dirty)
	var path, status string
	var received int64
	require.NoError(t, db.QueryRow("SELECT path FROM knowledge_folders WHERE id = 'company-folder'").Scan(&path))
	require.Equal(t, "公司/空目录", path)
	require.NoError(t, db.QueryRow("SELECT status, received_bytes FROM knowledge_upload_sessions WHERE id = 'company-upload'").Scan(&status, &received))
	require.Equal(t, "uploading", status)
	require.Equal(t, int64(4194304), received)
	for _, table := range versionedSQLiteTables {
		require.True(t, sqliteTableExists(t, db, table), table)
	}
	// 重复启动不应重放公司版或官方版迁移。
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
}
