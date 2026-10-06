package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv 는 스모크 테스트로 띄워진 자식 프로세스가 Bootstrap을 건너뛰게 한다.
//
// 엄밀히 말하면 이게 없어도 문제는 없다: 자식 프로세스의 os.Executable()은 artex.new 이고,
// 거기서 파생되는 모든 경로는 .new 접두사가 붙어 실제 업그레이드 파일을 건드리지 않는다. 하지만
// 이런 우연에 의존하는 건 너무 취약하므로, 명시적으로 단락시켜 한눈에 보이게 하고 자식 프로세스의
// 불필요한 디스크 탐색도 줄인다.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action 은 Bootstrap이 main에게 내리는 지시다.
type Action int

const (
	// Continue: 평소처럼 server를 시작한다.
	Continue Action = iota
	// Restart: 즉시 ExitRestart로 종료해, 감시 스크립트가 다시 띄우게 한다.
	Restart
)

// State 는 이번 시작 시점의 업그레이드 상태를 나타내며, /api/update/check 가 프런트엔드에
// "지난 업그레이드가 성공했는지, 아니면 롤백되었는지"를 그대로 전달하는 데 쓰인다.
type State struct {
	Pending     bool   // 교체 후 아직 안정성이 확인되지 않음
	RolledBack  bool   // 이번 시작에서 방금 자동 롤백이 실행됨
	FailedStage bool   // 임시 저장 파일의 검증/스모크 테스트를 통과하지 못해 폐기됨
	Detail      string // 사용자에게 보여줄 한 줄 설명
}

// Bootstrap 은 main의 맨 처음에서 실행되며, 어떤 포트를 리스닝하거나 데이터베이스를 열기 전에
// 반드시 호출해야 한다.
//
// 세 가지 경우:
//
//	① 임시 저장 파일 artex.new 가 존재  → 검증 + 스모크 테스트를 통과하면 교체 후 재시작 요청; 통과하지 못하면 폐기하고 이전 버전 계속 실행
//	② 마커 파일만 남아 있음             → 방금 교체가 끝났다는 뜻이므로 시도 횟수를 누적; 연속 실패가 충분히 많으면 롤백
//	③ 아무것도 없음                     → 정상 시작
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[update] 자동 부트스트랩을 건너뜁니다: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged 는 "임시 저장 파일이 존재"하는 상황을 처리한다: 검증을 통과하면 교체하고, 실패하면 폐기한다.
//
// 전체 업그레이드 흐름에서 실행 파일을 덮어쓰는 유일한 지점이자 마지막 관문이다 — 스모크 테스트가
// 다운로드 손상, 잘못된 아키텍처 선택, 동적 링크 누락 같은 문제를 막아준다. 실행되지 않는 바이너리를
// 한 번 통과시키면 감시 스크립트가 끝없이 그것을 다시 띄우기만 할 뿐 Go 코드는 실행될 기회조차 없고,
// 자동 롤백도 성립할 수 없게 된다.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[update] 임시 저장된 새 버전이 검증을 통과하지 못해 폐기하고, 현재 버전으로 계속 실행합니다: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "새 버전 검증 실패로 폐기됨: " + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[update] 교체 실패, 현재 버전으로 계속 실행합니다: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "교체 실패: " + err.Error()}
	}

	// 교체 성공. 마커를 남겨두어 다음 시작(새 버전으로 실행됨)에서 안정성을 확인하게 한다.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 업그레이드 마커 쓰기 실패(자동 롤백 기능을 잃습니다): %v", err)
	}
	log.Printf("[update] %s 로 교체 완료, 재시작을 위해 종료합니다(exit %d)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// confirmOrRollback 은 "교체 후 시작"을 처리한다: 시도 횟수를 누적하고, 한도를 넘으면 이전 버전으로 되돌린다.
//
// 횟수는 Go 코드가 실제로 실행된 후에만 증가하므로, 이것이 다루는 범위는 "실행은 되지만 초기화 중에
// 크래시"(설정 비호환, 포트 점유, DB 마이그레이션 실패) 같은 장애다; "exec 자체가 불가능한" 경우는
// 교체 전 스모크 테스트가 막아주며, 둘을 합쳐야 전체 커버리지가 완성된다.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			// 롤백까지 실패하면 더는 재시작하지 않는다. 그러지 않으면 무한 재시작에 빠진다. 마커를
			// 지우고 현재 상태로 프로세스를 띄운다 — 뜨지 않더라도 사용자가 로그에서 원인을 볼 수 있다.
			log.Printf("[update] 새 버전이 %d 회 연속 시작 실패했고, 롤백도 실패했습니다: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "새 버전 시작 실패 및 롤백 실패: " + err.Error()}
		}
		log.Printf("[update] 새 버전이 %d 회 연속 시작 실패하여 %s 로 롤백했고, 재시작을 위해 종료합니다(exit %d)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("새 버전 시작 실패로 %s 로 롤백했습니다", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[update] 업그레이드 마커 갱신 실패: %v", err)
	}
	log.Printf("[update] 새 버전 시작 중(%d/%d 번째 시도), 안정적으로 실행되면 업그레이드를 확정합니다",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle 은 새 버전이 안정적으로 실행 중임을 확인하고 업그레이드 마커를 제거한다.
//
// main이 HTTP 리스닝을 시작한 뒤 지연 호출한다: 이 시간을 버텨야만 인정되며, 그렇지 않으면 마커가
// 그대로 남아 다음 시작에서도 시도 횟수가 계속 누적되다가 결국 롤백이 트리거된다.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // 업그레이드 후의 시작이 아니므로 할 일이 없음
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[update] 업그레이드 마커 제거 실패: %v", err)
		return
	}
	log.Printf("[update] 새 버전이 안정적으로 실행되어 업그레이드가 완료되었습니다(이전 버전은 %s 로 보관됨)", p.Old)
}

// SettleDelay 는 "새 버전이 살아남았다"고 판정하는 데 필요한 실행 시간이다.
const SettleDelay = 30 * time.Second

// verifyStaged 는 임시 저장 파일을 검증한다: 먼저 SHA256을 대조하고, 그다음 실제로 한 번 실행해본다.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("체크섬 읽기: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("체크섬 계산: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 불일치(다운로드 손상 또는 변조됨)")
	}
	return smokeTest(p.New)
}

// smokeTest 는 -h 로 새 바이너리를 실행해, 현재 시스템에서 실제로 실행되는지 확인한다.
// 다운로드 손상, 잘못된 아키텍처 선택(exec format error), 의존성 누락 등의 문제 상당수를 막아준다.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("실행 권한 부여: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("스모크 테스트 시간 초과(새 바이너리가 응답하지 않음)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("스모크 테스트 실패: %v: %s", err, snippet)
	}
	return nil
}

// swap 은 현재 바이너리를 임시 저장된 새 버전으로 교체한다.
//
// Unix와 Windows 모두 실행 중인 실행 파일의 rename을 허용한다(Windows가 금지하는 것은 삭제와
// 덮어쓰기이며, rename은 여기에 포함되지 않는다), 그래서 플랫폼을 나눌 필요도, 자기 자신을 먼저
// 멈출 필요도 없다.
func swap(p Paths) error {
	// Windows의 rename은 이미 존재하는 대상을 덮어쓰지 않으므로, 이전 업그레이드가 남긴 .old를 먼저 지워야 한다.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("이전 백업 %s 정리: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("현재 버전 백업: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// 교체는 실패했지만 현재 버전은 이미 옮겨졌으므로, 그대로 되돌려 놓아야 한다. 그러지 않으면 다음 시작 시 실행 파일이 없다.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("새 버전 적용 실패(%v), 현재 버전 복구도 실패: %w", err, rerr)
		}
		return fmt.Errorf("새 버전 적용: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// rollback 은 swap이 백업해둔 이전 버전으로 되돌린다.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("롤백할 백업 %s 가 없습니다: %w", p.Old, err)
	}
	// 실행되지 않는 새 버전은 바로 삭제하지 않고 조사용으로 .failed 로 옮겨둔다.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("실패한 버전 이동: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("이전 버전 복구: %w", err)
	}
	return nil
}

// Rollback 은 /api/update/rollback 의 구현이다: 이전 버전으로 수동 복귀한다.
// 교체만 수행하며, 재시작은 마찬가지로 감시 스크립트에 맡긴다(호출자가 이어서 ExitRestart로 종료).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("롤백할 이전 버전이 없습니다(" + p.Old + " 가 존재하지 않음)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("이전 버전을 실행할 수 없어 롤백을 거부합니다: %w", err)
	}
	// 현재 버전과 백업을 교환한다: 롤백 후에도 다시 되돌릴 수 있도록.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("현재 버전 이동: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("이전 버전 적용: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[update] 롤백 후 백업 정리 실패(실행에는 영향 없음): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup 은 롤백 가능한 이전 버전이 존재하는지 알려주며, 프런트엔드가 롤백 버튼 표시 여부를 결정하는 데 쓰인다.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "알 수 없는 버전"
	}
	return s
}
