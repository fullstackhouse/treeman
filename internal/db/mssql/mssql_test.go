package mssql

import (
	"strings"
	"testing"

	"github.com/stubbedev/treeman/internal/config"
)

// TestDSNHidesNothingAndEscapes pins the two things a hand-built DSN
// gets wrong: a password with URL-significant characters, and an
// omitted database. SQL Server's own sa password policy pushes people
// toward `!`, `@` and `#`, so this is the common case, not the edge.
func TestDSNHidesNothingAndEscapes(t *testing.T) {
	got := dsn(config.MssqlConn{
		Host: "127.0.0.1", Port: 14333, User: "sa", Password: "p@ss/wo rd!#",
	}, "master")
	for _, want := range []string{
		"sqlserver://",
		"sa:p%40ss%2Fwo%20rd%21%23@127.0.0.1:14333",
		"database=master",
		"encrypt=disable",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn = %q, missing %q", got, want)
		}
	}
	// The password must never survive into the DSN unescaped, or the
	// `/` would re-anchor the URL and the `@` would split the host.
	if strings.Contains(got, "p@ss") {
		t.Errorf("dsn = %q: password is not escaped", got)
	}
}

// TestResolveServerDirsPrefersConfig covers the precedence the
// server-side directories follow, without a server: config over
// SERVERPROPERTY, data_dir standing in for a missing log path, backup
// falling back to data. Exercised through the same helpers Connect
// uses, since that is where a wrong fallback would land.
func TestResolveServerDirsPrefersConfig(t *testing.T) {
	cases := []struct {
		name                       string
		cfg                        config.MssqlConn
		data, logp, backup         string
		wantData, wantLog, wantBak string
		wantSep                    string
	}{
		{
			name:     "server defaults, posix",
			data:     "/var/opt/mssql/data/",
			logp:     "/var/opt/mssql/data/",
			backup:   "/var/opt/mssql/backup",
			wantData: "/var/opt/mssql/data",
			wantLog:  "/var/opt/mssql/data",
			wantBak:  "/var/opt/mssql/backup",
			wantSep:  "/",
		},
		{
			name:     "windows separator detected from the server's answer",
			data:     `C:\SQL\DATA\`,
			logp:     `C:\SQL\LOG\`,
			backup:   `C:\SQL\BACKUP`,
			wantData: `C:\SQL\DATA`,
			wantLog:  `C:\SQL\LOG`,
			wantBak:  `C:\SQL\BACKUP`,
			wantSep:  `\`,
		},
		{
			name:     "config wins, and data_dir stands in for the log dir",
			cfg:      config.MssqlConn{DataDir: "/mnt/fast", BackupDir: "/mnt/bak"},
			data:     "/var/opt/mssql/data/",
			logp:     "/var/opt/mssql/log/",
			backup:   "/var/opt/mssql/backup",
			wantData: "/mnt/fast",
			wantLog:  "/mnt/fast",
			wantBak:  "/mnt/bak",
			wantSep:  "/",
		},
		{
			name:     "no backup path reported falls back to the data dir",
			data:     "/var/opt/mssql/data/",
			logp:     "/var/opt/mssql/data/",
			wantData: "/var/opt/mssql/data",
			wantLog:  "/var/opt/mssql/data",
			wantBak:  "/var/opt/mssql/data",
			wantSep:  "/",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Driver{cfg: c.cfg}
			if err := d.applyServerDirs(c.data, c.logp, c.backup); err != nil {
				t.Fatalf("applyServerDirs: %v", err)
			}
			if d.dataDir != c.wantData || d.logDir != c.wantLog || d.backupDir != c.wantBak {
				t.Errorf("dirs = (%q, %q, %q), want (%q, %q, %q)",
					d.dataDir, d.logDir, d.backupDir, c.wantData, c.wantLog, c.wantBak)
			}
			if d.sep != c.wantSep {
				t.Errorf("sep = %q, want %q", d.sep, c.wantSep)
			}
		})
	}
}

// TestResolveServerDirsRefusesEmpty: a server that reports no default
// paths at all (an unusual install, or a login without VIEW SERVER
// STATE) must fail at Connect with the remedy named, not later with a
// RESTORE writing to the filesystem root.
func TestResolveServerDirsRefusesEmpty(t *testing.T) {
	d := &Driver{}
	err := d.applyServerDirs("", "", "")
	if err == nil {
		t.Fatal("empty server paths must be refused")
	}
	for _, want := range []string{"data_dir", "backup_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestBackupPathIsOneFilePerTemplate pins the naming the GC leans on:
// one `.bak` per template name, so the files on disk are bounded by the
// live templates and a re-capture overwrites rather than accumulates.
func TestBackupPathIsOneFilePerTemplate(t *testing.T) {
	d := &Driver{}
	if err := d.applyServerDirs("/var/opt/mssql/data/", "", "/bak"); err != nil {
		t.Fatal(err)
	}
	if got, want := d.backupPath("_tm_0123456789abcdef"), "/bak/_tm_0123456789abcdef.bak"; got != want {
		t.Errorf("backupPath = %q, want %q", got, want)
	}
	if d.backupPath("_tm_a") == d.backupPath("_tm_b") {
		t.Error("two templates share one backup file")
	}
}

// TestFileTokenIsRandom: restored files are named after a token rather
// than after the database, because a claimed spare keeps files named
// for the spare. Two tokens colliding would reintroduce exactly the
// collision the token exists to avoid.
func TestFileTokenIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok, err := fileToken()
		if err != nil {
			t.Fatalf("fileToken: %v", err)
		}
		if len(tok) != 8 {
			t.Fatalf("fileToken = %q, want 8 hex chars", tok)
		}
		if seen[tok] {
			t.Fatalf("fileToken repeated %q within 100 draws", tok)
		}
		seen[tok] = true
	}
}

// TestCellString covers the one conversion RESTORE FILELISTONLY forces:
// its columns arrive as `any`, and a driver that hands back []byte
// instead of string would silently yield an empty logical file name —
// which RESTORE would then reject with a message about a file that is
// not in the backup set.
func TestCellString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"logical", "logical"},
		{[]byte("logical"), "logical"},
		{nil, ""},
		{42, ""},
	}
	for _, c := range cases {
		if got := cellString(c.in); got != c.want {
			t.Errorf("cellString(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}
