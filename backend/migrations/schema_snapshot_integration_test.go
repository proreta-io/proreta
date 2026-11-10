package migrations_test

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.sql from the migrated database")

const snapshotFile = "testdata/schema.sql"

// snapshotQueries read the schema of public from the catalog, one line per object, in a stable
// order. They use the pg_get_*def functions instead of pg_dump, whose output changes between
// major versions; the monthly check_results partitions are left out (their names follow the
// calendar).
var snapshotQueries = []struct{ section, sql string }{
	{"tables", `
SELECT c.relname || ' kind=' || c.relkind::text || ' owner=' || pg_get_userbyid(c.relowner)
       || ' rls=' || c.relrowsecurity || ' force=' || c.relforcerowsecurity
       || COALESCE(' options=' || array_to_string(c.reloptions, ','), '')
       || COALESCE(' partition by ' || pg_get_partkeydef(c.oid), '')
       || COALESCE(' acl=' || NULLIF(array_to_string(ARRAY(SELECT a::text FROM unnest(c.relacl) AS a ORDER BY a::text COLLATE "C"), ','), ''), '')
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S') AND c.relname !~ '^check_results_p[0-9]{6}$'
ORDER BY c.relname COLLATE "C"`},
	{"columns", `
SELECT c.relname || '.' || a.attnum || ' ' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
       || CASE WHEN a.attnotnull THEN ' not null' ELSE '' END
       || COALESCE(' default ' || pg_get_expr(d.adbin, d.adrelid), '')
       || CASE WHEN a.attidentity <> '' THEN ' identity ' || a.attidentity::text ELSE '' END
       || COALESCE(' acl=' || NULLIF(array_to_string(ARRAY(SELECT x::text FROM unnest(a.attacl) AS x ORDER BY x::text COLLATE "C"), ','), ''), '')
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid AND c.relkind IN ('r', 'p')
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attnum > 0 AND NOT a.attisdropped AND c.relname !~ '^check_results_p[0-9]{6}$'
ORDER BY c.relname COLLATE "C", a.attnum`},
	{"constraints", `
SELECT c.relname || ' ' || k.conname || ' ' || pg_get_constraintdef(k.oid)
       || CASE WHEN k.convalidated THEN '' ELSE ' NOT VALID' END
FROM pg_constraint k
JOIN pg_class c ON c.oid = k.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.relname !~ '^check_results_p[0-9]{6}$'
ORDER BY c.relname COLLATE "C", k.conname COLLATE "C"`},
	{"indexes", `
SELECT pg_get_indexdef(i.indexrelid)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indrelid
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.relname !~ '^check_results_p[0-9]{6}$'
ORDER BY ic.relname COLLATE "C"`},
	{"policies", `
SELECT c.relname || ' ' || p.polname || ' cmd=' || p.polcmd::text || ' permissive=' || p.polpermissive
       || ' to=' || array_to_string(ARRAY(SELECT CASE WHEN r = 0 THEN 'public' ELSE pg_get_userbyid(r)::text END FROM unnest(p.polroles) AS r ORDER BY (CASE WHEN r = 0 THEN 'public' ELSE pg_get_userbyid(r)::text END) COLLATE "C"), ',')
       || COALESCE(' using ' || pg_get_expr(p.polqual, p.polrelid), '')
       || COALESCE(' check ' || pg_get_expr(p.polwithcheck, p.polrelid), '')
FROM pg_policy p
JOIN pg_class c ON c.oid = p.polrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE c.relname !~ '^check_results_p[0-9]{6}$'
ORDER BY c.relname COLLATE "C", p.polname COLLATE "C"`},
	{"triggers", `
SELECT pg_get_triggerdef(t.oid)
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
WHERE NOT t.tgisinternal
ORDER BY c.relname COLLATE "C", t.tgname COLLATE "C"`},
	{"function privileges", `
SELECT p.oid::regprocedure::text || ' owner=' || pg_get_userbyid(p.proowner) || ' definer=' || p.prosecdef
       || ' acl=' || COALESCE(array_to_string(ARRAY(SELECT a::text FROM unnest(p.proacl) AS a ORDER BY a::text COLLATE "C"), ','), 'default')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname = 'public'
WHERE NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
ORDER BY p.oid::regprocedure::text COLLATE "C"`},
	{"default privileges", `
SELECT line FROM (
    SELECT pg_get_userbyid(d.defaclrole) || ' ' || COALESCE(n.nspname, '*') || ' ' || d.defaclobjtype::text || ' '
           || array_to_string(ARRAY(SELECT a::text FROM unnest(d.defaclacl) AS a ORDER BY a::text COLLATE "C"), ',') AS line
    FROM pg_default_acl d LEFT JOIN pg_namespace n ON n.oid = d.defaclnamespace
) q
ORDER BY line COLLATE "C"`},
	{"functions", `
SELECT pg_get_functiondef(p.oid)
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace AND n.nspname = 'public'
WHERE NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
ORDER BY p.oid::regprocedure::text COLLATE "C"`},
}

func schemaSnapshot(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("-- Catalog snapshot of the migrated schema, compared by TestIntegrationSchemaSnapshot.\n")
	b.WriteString("-- Regenerate: TEST_DATABASE_ADMIN_URL=... go test ./migrations -run TestIntegrationSchemaSnapshot -update\n")
	for _, q := range snapshotQueries {
		rows, err := conn.Query(context.Background(), q.sql)
		if err != nil {
			t.Fatalf("snapshot %s: %v", q.section, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("snapshot %s: %v", q.section, err)
		}
		b.WriteString("\n-- " + q.section + "\n")
		for _, l := range lines {
			b.WriteString(strings.TrimRight(l, "\n") + "\n")
		}
	}
	return b.String()
}

// TestIntegrationSchemaSnapshot compares the schema the migrations build with the committed
// snapshot in testdata/schema.sql. Any change to the schema shows up as a diff of that file in
// review: a new migration (or a changed function) needs the snapshot regenerated with -update.
func TestIntegrationSchemaSnapshot(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("snapshot"))
	got := schemaSnapshot(t, env.AdminConn(t))
	if *update {
		if err := os.WriteFile(snapshotFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(snapshotFile)
	if err != nil {
		t.Fatalf("%v; create it with -update", err)
	}
	if got == string(want) {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := 0; i < max(len(gl), len(wl)); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			t.Fatalf("schema differs from %s at line %d:\n  got:  %s\n  want: %s\nrun with -update after reviewing the change", snapshotFile, i+1, g, w)
		}
	}
}
