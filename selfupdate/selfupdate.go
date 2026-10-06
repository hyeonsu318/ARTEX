// Package selfupdate implements ARTEX의 페이지 원클릭 업데이트: GitHub Release에서 새 버전
// 바이너리를 받아 검증하고 임시 저장한 뒤, 다음 시작 시 원자적으로 교체한다.
//
// 전체 역할 분담(start.sh / start.bat 참고):
//
//	시작 스크립트 = 단순한 감시 루프, "프로세스 종료 후 종료 코드로 재기동 여부만 결정"
//	이 패키지     = 실수하기 쉬운 로직 전부(다운로드 / SHA256 검증 / 스모크 테스트 / 교체 / 실패 롤백)
//
// 교체를 스크립트가 아니라 Go에 둔 이유는, sha256 검증과 스모크 테스트를 sh와 bat에서
// 각각 두 벌로 작성해야 하는데(sha256sum / shasum / certutil), 이 부분이 바로 가장 실수가
// 있으면 안 되는 지점이기 때문이다 — 실행되지 않는 바이너리로 교체되면 감시 프로세스가
// 충실하게 그것을 반복해서 띄우기만 할 뿐이라, 사용자는 직접 기기에서 수동으로 복구해야 한다.
//
// 완전한 업그레이드 한 번에 프로세스가 세 번 시작된다:
//
//	① 이전 버전 server가 /api/update/apply 를 받음 → 다운로드 및 검증 → artex.new 로 임시 저장 → exit 75
//	② 스크립트가 이전 버전을 다시 띄움 → Bootstrap이 artex.new 를 발견 → 검증+스모크 테스트 → 교체 → exit 75
//	③ 스크립트가 다시 띄움, 이때는 이미 새 버전 → Bootstrap이 시도 횟수를 기록 → 시작 성공 후 마커 제거
//
// 어느 단계든 실패하면 이전 버전으로 되돌린다: ② 검증 실패 시 임시 저장 파일을 지우고 이전 버전을
// 계속 실행; ③ 연속 3회 마커 제거(=안정적으로 살아남음)에 이르지 못하면(뜨지 못하고 죽으면)
// artex.old 로 자동으로 되돌린다.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart 는 "감시 프로세스에게 나를 다시 띄워달라"는 종료 코드다(EX_TEMPFAIL). 시작
// 스크립트는 이 코드를 보면 즉시 재실행하며, 크래시 지연에 포함시키지 않는다. 0은 사용자가
// 정상적으로 정지한 것(스크립트가 루프를 종료), 그 외는 모두 크래시로 간주한다.
const ExitRestart = 75

// maxAttempts 는 교체 후 허용되는 시작 시도 횟수다. 새 버전은 시작할 때마다 횟수를 +1 하며,
// settleDelay 를 넘겨 살아남으면 마커를 지운다; maxAttempts 만큼 연속으로 크래시하면 새 버전이
// 전혀 뜨지 못한다는 뜻이므로 자동으로 롤백한다.
const maxAttempts = 3

// Paths 는 한 번의 업그레이드에 관련된 모든 파일을 가리키며, 전부 **실행 파일이 있는 디렉터리** 아래에 둔다.
// 의도적으로 CWD를 쓰지 않는다: 서비스로 실행될 때 작업 디렉터리가 / 이거나 임의의 경로일 수 있어,
// CWD를 쓰면 임시 저장 파일이 다른 곳에 놓여 교체 로직이 완전히 무력화된다.
type Paths struct {
	Dir     string // 실행 파일이 있는 디렉터리
	Current string // 현재 실행 중인 바이너리        artex      / artex.exe
	New     string // 임시 저장된 새 버전            artex.new  / artex.new.exe
	Sum     string // 새 버전의 sha256(hex)         artex.new.sha256 / artex.new.exe.sha256
	Old     string // 교체 전 백업된 이전 버전       artex.old  / artex.old.exe
	Marker  string // 업그레이드 상태 마커           artex.upgrade.json
}

// ResolvePaths 는 현재 실행 파일을 기준으로 모든 업그레이드 경로를 계산한다.
//
// Windows에서는 .new/.old 도 .exe 접미사를 가져야 한다. 그러지 않으면 스모크 테스트와 교체 후
// 실행이 모두 실패하므로, 먼저 접미사를 떼어내고 다시 붙여서 두 플랫폼의 네이밍을 대칭으로 만든다.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("실행 파일 위치 확인: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Windows에서는 ".exe", Unix에서는 보통 빈 문자열
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker 는 한 번의 교체 진행 상황을 기록하며, 새 버전이 뜨지 못할 때 자동 롤백을 트리거하는 데 쓰인다.
type marker struct {
	From     string `json:"from"`     // 업그레이드 전 버전
	To       string `json:"to"`       // 목표 버전
	Attempts int    `json:"attempts"` // 교체 후 시작을 시도한 횟수
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged 는 임시 저장 파일을 정리한다. 교체 성공, 검증 실패, 사용자 취소 모두 이 경로를
// 거치며, 남은 artex.new 가 다음 시작에서 다시 시도되는 것을 막는다.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions 는 두 버전 번호를 비교해 -1/0/1 을 반환한다(a<b / a==b / a>b).
// ok=false 는 둘 중 하나가 비교 가능한 버전 번호가 아니라는 뜻이다(예: 로컬 개발 빌드의 "dev" 나
// git describe 가 만든 "0.3.7-2-gabc1234-dirty"). 이 경우 호출자는 원클릭 업데이트를 비활성화해야
// 한다. 그러지 않으면 개발 중인 빌드를 정식 버전으로 "업그레이드"해 커밋되지 않은 변경을 덮어쓰게 된다.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion 은 "v0.3.7" / "0.3.7" 형태의 버전 번호를 [3]int 로 파싱한다.
//
// 순수한 세 자리 형식만 허용한다: build.sh 가 tag가 아닌 빌드에서 git describe 로 만든
// "0.3.7-2-gabc1234" 같은 접미사 붙은 버전은 반드시 비교 불가능으로 판정해야 하며, 0.3.7 로
// 취급해서는 안 된다 — 그러지 않으면 개발 빌드가 "이미 최신"으로 오판되거나 정식 버전에 덮어써진다.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker 는 프로세스가 컨테이너 안에서 실행 중인지 알려준다. Docker에서는 교체가 컨테이너의
// 쓰기 가능 레이어에 기록되므로, `docker compose up -d` 로 컨테이너를 재생성하면 이미지에 내장된
// 버전으로 되돌아간다 — 이는 의도된 동작이다(그 시점에는 사용자가 어차피 새 이미지를 받는 중이다),
// 다만 프런트엔드가 이를 바탕으로 상황을 명확히 설명할 수 있어야 한다.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
