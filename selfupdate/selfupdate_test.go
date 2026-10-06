package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths 는 격리된 업그레이드 디렉터리를 만든다. ResolvePaths()를 직접 쓸 수 없다 — 그러면
// 테스트 바이너리 자체를 가리키게 되어, 실행하는 순간 go test의 실행 파일 이름이 바뀌어 버린다.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Dir:     dir,
		Current: filepath.Join(dir, "artex"),
		New:     filepath.Join(dir, "artex.new"),
		Sum:     filepath.Join(dir, "artex.new.sha256"),
		Old:     filepath.Join(dir, "artex.old"),
		Marker:  filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin 은 artex를 가장할 실행 가능한 쉘 스크립트를 하나 작성한다. smokeTest는 -h 로 실행해
// 종료 코드만 확인하므로, 스크립트만으로도 충분하고 실제 바이너리를 컴파일하는 것보다 훨씬 빠르다.
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("가짜 바이너리 %s 쓰기: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// stage 는 bin을 "임시 저장되어 교체를 기다리는" 상태로 꾸민다: artex.new 와 그 체크섬을 작성한다.
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err := fileSHA256(p.New)
	if err != nil {
		t.Fatalf("체크섬 계산: %v", err)
	}
	if err := os.WriteFile(p.Sum, []byte(sum), 0o644); err != nil {
		t.Fatalf("체크섬 쓰기: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s 읽기: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("가짜 바이너리는 sh 스크립트를 사용하므로 Windows에서는 실행할 수 없음")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // build.sh는 v를 떼고, tag는 v를 붙이므로 양쪽 다 인식해야 함
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // 숫자 비교여야 하고 사전식 비교가 아니어야 함
		{"1.0.0", "0.99.99", 1, true},
		// 개발 빌드는 비교 불가능으로 판정해야 한다. 그러지 않으면 정식 버전이 커밋되지 않은 변경을 덮어쓴다.
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, 기대값 %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, 기대값 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// 핵심 불변 조건: 모든 업그레이드 파일은 실행 파일과 같은 디렉터리에 있어야 한다. CWD에 놓이면
	// 서비스로 실행될 때(작업 디렉터리가 / 일 수 있음) 교체가 완전히 실패한다.
	for name, path := range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s 가 실행 파일 디렉터리 안에 있지 않음: %s (기대값 %s)", name, path, p.Dir)
		}
	}
	// Windows에서는 .new/.old가 .exe를 유지해야 하며, 그렇지 않으면 스모크 테스트와 교체 후 실행이 모두 실패한다.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(p.New, ".exe") || !strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("Windows에서는 .new/.old 가 .exe 로 끝나야 함: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "new", 0)

	// 체크섬을 쓴 다음 파일을 변경해, 다운로드 손상 / 바꿔치기 상황을 흉내낸다.
	fakeBin(t, p.New, "tampered", 0)
	if err := verifyStaged(p); err == nil {
		t.Fatal("SHA256 불일치로 거부되길 기대했는데 통과함")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "broken", 1) // 실행은 되지만 종료 코드가 0이 아님

	if err := verifyStaged(p); err == nil {
		t.Fatal("스모크 테스트 실패로 거부되길 기대했는데 통과함")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err != nil {
		t.Fatalf("마커 쓰기: %v", err)
	}

	action, st := applyStaged(p)
	if action != Restart {
		t.Fatalf("Restart 를 기대했는데 %v 를 받음", action)
	}
	if !st.Pending {
		t.Error("교체 후 상태는 Pending 이어야 함")
	}
	if !strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex 는 새 버전으로 교체되어 있어야 함")
	}
	if !strings.Contains(readAll(t, p.Old), "old") {
		t.Error("이전 버전은 artex.old 로 백업되어 있어야 함")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("교체 후 artex.new 는 사라져 있어야 함")
	}
	if _, err := os.Stat(p.Sum); !os.IsNotExist(err) {
		t.Error("교체 후 체크섬 파일은 정리되어 있어야 함")
	}
	// 마커는 남겨둬야 한다. 다음 시작(새 버전으로 실행됨)에서 이걸로 횟수를 세고, 필요하면 롤백한다.
	if _, ok := readMarker(p.Marker); !ok {
		t.Error("교체 후 업그레이드 마커는 남아 있어야 함")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // 체크섬을 깨뜨림

	action, st := applyStaged(p)
	if action != Continue {
		t.Fatalf("검증 실패 시 Continue 를 기대했는데 %v 를 받음", action)
	}
	if !st.FailedStage {
		t.Error("상태는 FailedStage 로 표시되어야 함")
	}
	if !strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("검증 실패 시 현재 버전은 절대 건드리면 안 됨")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("검증 실패한 임시 저장 파일은 정리되어야 함. 그러지 않으면 다음 시작에서 다시 시도함")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // 이전 업그레이드가 남긴 백업
	stage(t, p, "v3", 0)

	if err := swap(p); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("v3 로 교체되어 있어야 함")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("백업은 방금 교체되어 내려간 v2 로 갱신되어 있어야 함")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m := marker{From: "0.3.7", To: "0.3.8"}

	// 처음 maxAttempts 번은 횟수만 누적하며, 새 버전이 스스로 안정화될 기회를 준다.
	for i := 1; i <= maxAttempts; i++ {
		action, st := confirmOrRollback(p, m)
		if action != Continue {
			t.Fatalf("%d 번째 시도에서 Continue 를 기대했는데 %v 를 받음", i, action)
		}
		if !st.Pending {
			t.Errorf("%d 번째 시도 상태는 Pending 이어야 함", i)
		}
		got, ok := readMarker(p.Marker)
		if !ok || got.Attempts != i {
			t.Fatalf("%d 번째 시도 후 attempts=%d(ok=%v), 기대값 %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// 한 번 더 깨지면 한도를 초과해, 자동으로 이전 버전으로 되돌린다.
	action, st := confirmOrRollback(p, m)
	if action != Restart {
		t.Fatalf("시도 횟수 초과 시 Restart 를 기대했는데 %v 를 받음", action)
	}
	if !st.RolledBack {
		t.Error("상태는 RolledBack 으로 표시되어야 함")
	}
	if !strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("이전 버전으로 롤백되어 있어야 함")
	}
	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Error("롤백 후 마커는 제거되어야 함. 그러지 않으면 무한 롤백에 빠짐")
	}
	// 실행되지 않는 그 버전은 조사용으로 남겨두고 바로 삭제하지 않는다.
	if _, err := os.Stat(p.Current + ".failed"); err != nil {
		t.Error("실패한 버전은 조사를 위해 .failed 로 보관되어야 함")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback() 은 ResolvePaths() 를 거치므로, 여기서는 하부의 교환 로직을 직접 테스트한다.
	tmp := p.Current + ".swap"
	if err := os.Rename(p.Current, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("롤백 후 현재 버전은 v1 이어야 함")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("롤백 후 백업은 v2 가 되어야 함. 그래야 다시 되돌릴 수 있음")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum   = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	// sha256sum 출력은 공백 두 개로 구분됨; shasum -a 256 의 바이너리 모드는 파일명 앞에 * 를 붙임.
	raw := linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // 정확히 두 필드지만 첫 번째가 다이제스트가 아님
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // 다이제스트 길이가 맞지 않음

	out := parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"] != linuxSum {
		t.Errorf("linux 항목 파싱 오류: %v", out)
	}
	// 다이제스트는 소문자로 통일되어야, 대소문자 차이로 불일치 오판을 하지 않음.
	if got := out["artex-0.3.8-windows-amd64.zip"]; got != strings.ToLower(winSum) {
		t.Errorf("windows 항목 오류(* 접두사는 제거되고 다이제스트는 소문자로 변환되어야 함): %q", got)
	}
	if len(out) != 2 {
		t.Errorf("빈 줄, 다이제스트가 아닌 줄, 길이가 틀린 줄은 무시되어야 하는데 %v 를 받음", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("패키지 내 기본 파일명이 Windows에서는 artex.exe 이므로, 이 테스트는 Unix 네이밍으로 구성됨")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// 실제 배포 패키지의 구조: artex-<버전>-<os>-<arch>/artex, 그리고 교란용 파일 몇 개.
	for name, body := range map[string]string{
		"artex-0.3.8-linux-amd64/README.md":           "readme",
		"artex-0.3.8-linux-amd64/skills/a.md":         "skill",
		"artex-0.3.8-linux-amd64/artex":               "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extractBinary(zipPath, dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got := readAll(t, dst); !strings.Contains(got, "exit 0") {
		t.Errorf("압축 해제된 것이 artex 실행 파일이 아님: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("압축 해제된 바이너리는 실행 권한을 가지고 있어야 함")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err := extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("패키지 안에 실행 파일이 없으면 에러가 나야 함")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad := []string{
		"http://github.com/x",           // HTTPS가 아님
		"https://evil.com/artex.zip",    // 도메인이 화이트리스트에 없음
		"https://github.com.evil.com/x", // 접미사 위장
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw := range bad {
		u := mustParse(t, raw)
		if err := checkURL(u); err == nil {
			t.Errorf("checkURL(%q) 는 거부되어야 함", raw)
		}
	}
	good := []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // 도메인은 대소문자 구분 없음
	}
	for _, raw := range good {
		u := mustParse(t, raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) 는 허용되어야 하는데 에러가 남: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// build.sh 의 package_binary 는 artex-<버전>-<os>-<arch>.zip 을 사용하며, 버전 번호는
	// v 접두사가 제거되어 있다. 여기서 한 글자라도 틀리면 모든 플랫폼의 원클릭 업데이트가 자산을 찾지 못한다.
	if got := AssetName("v0.3.8", "linux", "amd64"); got != "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("0.3.8", "windows", "amd64"); got != "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("%q 파싱: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Fatal("안정성 확인 후 업그레이드 마커는 반드시 제거되어야 함")
	}
	// 마커가 없으면, 이후 정상 재시작에서 횟수가 다시 누적되거나 롤백이 잘못 트리거되지 않음.
	if _, ok := readMarker(p.Marker); ok {
		t.Error("마커 읽기는 실패해야 함")
	}
	// 백업은 남겨둬야 사용자가 수동으로 롤백할 수 있음.
	if _, err := os.Stat(p.Old); err != nil {
		t.Error("안정성 확인 후에도 이전 버전 백업은 남아 있어야 함")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // 일반 시작 경로이므로 panic도, 파일 변경도 없어야 함
	if _, err := os.Stat(p.Current); err != nil {
		t.Error("마커가 없을 때 settle 은 어떤 파일에도 영향을 주지 않아야 함")
	}
}
