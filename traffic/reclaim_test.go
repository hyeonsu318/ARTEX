package traffic

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bulkRecord fills the index with inline bodies — the ones that actually make
// index.sqlite grow. Binary content type keeps them out of the full-text index so
// the test stays fast; the FTS side is covered by TestReclaimMergesFTSTombstones.
func bulkRecord(tr *Traffic, host string, n, size int) {
	body := []byte(strings.Repeat("A", size))
	for i := 0; i < n; i++ {
		tr.record(newFlow(host, "GET", fmt.Sprintf("/blob/%d", i), nil, body,
			withRespType("application/octet-stream")))
	}
}

func TestNewIndexEnablesIncrementalVacuum(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.incrementalVacuum {
		t.Fatal("새로 만든 인덱스 DB에 증분 회수가 켜져 있지 않다")
	}
	var mode int
	if err := tr.DB().QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != autoVacuumIncremental {
		t.Fatalf("auto_vacuum=%d, %d 이어야 한다", mode, autoVacuumIncremental)
	}
}

// TestDeleteReclaimsIndexSpace is the regression: deleting traffic used to leave
// index.sqlite at its high-water mark forever, because SQLite only chains freed
// pages onto its freelist and nothing ever returned them to the filesystem.
func TestDeleteReclaimsIndexSpace(t *testing.T) {
	tr, _ := openTraffic(t)
	const host = "bulk.example.com"
	// 30 × 200KB stays under maxInlineBody, so every body lands in the database
	// itself rather than the blob store — that is where the growth was invisible.
	bulkRecord(tr, host, 30, 200*1024)
	grown := tr.indexBytes()
	if grown < 5<<20 {
		t.Fatalf("인덱스가 %d 바이트뿐이라 회수를 검증하기에 샘플이 부족하다", grown)
	}

	if n, err := tr.DeleteHostsExact([]string{host}); err != nil || n != 30 {
		t.Fatalf("DeleteHostsExact=(%d,%v), (30,nil)이어야 한다", n, err)
	}
	tr.reaping.Wait() // 회수는 백그라운드에서 청크 단위로 진행된다

	after := tr.indexBytes()
	if after > grown/4 {
		t.Fatalf("삭제 후에도 인덱스가 %d 바이트를 차지한다(삭제 전 %d), 공간이 파일시스템에 반환되지 않았다", after, grown)
	}
	// A handful of pages incremental_vacuum could not move to the end of the file
	// is a normal residual; the ~1500 that the deletion freed must be gone.
	var free int
	if err := tr.DB().QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free > 64 {
		t.Fatalf("빈 페이지 %d 개가 아직 회수되지 않았다", free)
	}
}

// TestReclaimMergesFTSTombstones covers the second half of the leak: ex_fts is a
// contentless_delete index, so a DELETE only writes tombstones. Without a merge
// the index keeps growing on every deletion — deleting traffic made it bigger.
func TestReclaimMergesFTSTombstones(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.fts {
		t.Skip("드라이버에 FTS5가 켜져 있지 않다")
	}
	// Deleted in batches, which is what leaves tombstones spread over many
	// segments rather than emptying the index in one shot.
	for round := 0; round < 4; round++ {
		host := fmt.Sprintf("fts%d.example.com", round)
		for i := 0; i < 20; i++ {
			tr.record(newFlow(host, "GET", fmt.Sprintf("/p/%d", i), nil,
				[]byte(strings.Repeat("secret token 한글본문 padding ", 200))))
		}
		if _, err := tr.DeleteHostsExact([]string{host}); err != nil {
			t.Fatal(err)
		}
		tr.reaping.Wait()
	}

	var exchanges, segments int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&exchanges); err != nil {
		t.Fatal(err)
	}
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM ex_fts_data`).Scan(&segments); err != nil {
		t.Fatal(err)
	}
	if exchanges != 0 {
		t.Fatalf("트래픽이 %d 건 남아 있다", exchanges)
	}
	// A fully merged, empty contentless index keeps only its structure rows.
	if segments > 8 {
		t.Fatalf("전문 인덱스에 세그먼트 데이터 %d 행이 남아 있다, tombstone이 병합·회수되지 않았다", segments)
	}
}

// TestReclaimOnLegacyIndexIsHarmless covers installs created before
// auto_vacuum=incremental became the default: incremental_vacuum is a silent
// no-op there, so reclamation must report the situation and finish rather than
// spin or fail. Only a full compaction can convert such a file.
func TestReclaimOnLegacyIndexIsHarmless(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "_index"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the tables first, with auto_vacuum left at its default 0 — exactly the
	// shape Open used to leave behind.
	legacy, err := sql.Open("sqlite", filepath.Join(dir, "_index", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(indexSchema); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if tr.incrementalVacuum {
		t.Fatal("기존 DB는 증분 회수가 켜졌다고 보고하면 안 된다")
	}

	const host = "legacy.example.com"
	bulkRecord(tr, host, 8, 200*1024)
	if n, err := tr.DeleteHostsExact([]string{host}); err != nil || n != 8 {
		t.Fatalf("DeleteHostsExact=(%d,%v), (8,nil)이어야 한다", n, err)
	}
	tr.reaping.Wait() // 반드시 수렴해야 하며, 예산에 걸려 멈추면 안 된다

	// The freelist stays populated: that is the whole reason a compaction entry
	// point is needed for pre-existing databases.
	var free int
	if err := tr.DB().QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free == 0 {
		t.Fatal("기존 DB가 빈 페이지를 회수했다, 테스트가 실제로 기존 DB를 구성하지 못했다는 뜻이다")
	}
}

// TestDeleteAllPurgesAndCompacts covers the page's clear-everything action: it
// must leave nothing behind — including host directories the index no longer
// knows about — and it must hand the index space back, since an emptied index is
// the one moment a full rewrite is cheap.
func TestDeleteAllPurgesAndCompacts(t *testing.T) {
	tr, dir := openTraffic(t)
	bulkRecord(tr, "a.example.com", 10, 200*1024)
	bulkRecord(tr, "b.example.com", 10, 200*1024)
	// A text body so the full-text index has real content, and a spilled one so a
	// blob exists to collect.
	tr.record(newFlow("c.example.com", "GET", "/page", nil, []byte(strings.Repeat("secret-token ", 500))))
	tr.record(newFlow("c.example.com", "GET", "/big", nil,
		[]byte(strings.Repeat("B", maxInlineBody+1024)), withRespType("application/sql")))
	// An orphaned legacy directory: no index row points at it, so only a
	// clear-everything should take it.
	orphan := filepath.Join(dir, "orphan.example.com")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	grown := tr.indexBytes()
	if grown < 5<<20 {
		t.Fatalf("인덱스가 %d 바이트뿐이라 샘플이 부족하다", grown)
	}

	deleted, reclaimed, err := tr.DeleteAll()
	if err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if deleted != 22 {
		t.Fatalf("deleted=%d, 22여야 한다", deleted)
	}
	tr.reaping.Wait()

	if reclaimed < grown/2 {
		t.Fatalf("%d 바이트만 회수되었다(삭제 전 인덱스 %d)", reclaimed, grown)
	}
	if after := tr.indexBytes(); after > grown/8 {
		t.Fatalf("비운 뒤에도 인덱스가 %d 바이트를 차지한다(삭제 전 %d)", after, grown)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM exchanges`,
		`SELECT COUNT(*) FROM exchange_bodies`,
		`SELECT COUNT(*) FROM blob_refs`,
	} {
		var c int
		if err := tr.DB().QueryRow(q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("%s = %d, 0이어야 한다", q, c)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("고아가 된 과거 host 디렉터리가 정리되지 않았다: %v", err)
	}
	// Recording must keep working against the freshly rewritten file.
	tr.record(newFlow("d.example.com", "GET", "/after", nil, []byte("비운 뒤에도 녹화 가능")))
	if n, err := tr.Count(); err != nil || n != 1 {
		t.Fatalf("비운 뒤 Count=(%d,%v), (1,nil)이어야 한다", n, err)
	}
}

// TestDeleteAllConvertsLegacyIndex is why the purge compacts rather than just
// deleting: auto_vacuum cannot be switched on after the fact except through a
// VACUUM, and an emptied index is the cheapest place to pay for one. After this,
// ordinary deletions reclaim space on their own.
func TestDeleteAllConvertsLegacyIndex(t *testing.T) {
	dir := t.TempDir()
	old := openLegacyIndex(t, dir)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if tr.incrementalVacuum {
		t.Fatal("기존 DB는 증분 회수가 켜졌다고 보고하면 안 된다")
	}

	bulkRecord(tr, "legacy.example.com", 10, 200*1024)
	if _, _, err := tr.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if !tr.incrementalVacuum {
		t.Fatal("비운 뒤에도 기존 DB가 증분 회수 모드로 전환되지 않았다")
	}

	// The converted database now reclaims on an ordinary host deletion.
	bulkRecord(tr, "again.example.com", 10, 200*1024)
	grown := tr.indexBytes()
	if _, err := tr.DeleteHostsExact([]string{"again.example.com"}); err != nil {
		t.Fatal(err)
	}
	tr.reaping.Wait()
	if after := tr.indexBytes(); after > grown/4 {
		t.Fatalf("전환 후 일반 삭제에서도 회수되지 않았다: %d 바이트(삭제 전 %d)", after, grown)
	}
}
