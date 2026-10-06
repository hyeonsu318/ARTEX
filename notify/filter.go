package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter 는 notification_channels.filter JSONB 컬럼의 계약이다: 채널 인스턴스의
// 필터 조건. 모든 필드는 선택이며, 비워두면 "필터링하지 않음"이다——이것이
// 바로 잘못된 설정의 기본 동작이며, ParseFilter 참고.
type Filter struct {
	// MinSeverity 는 최저 등급 기준이다(low/medium/high/critical), 비어 있으면
	// 기준 없음.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs 가 빈 배열이면 제한 없음; 값이 있으면 이벤트가 그와
	// 교집합을 가져야 한다.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude 가 비어 있으면 전부 받음; 값이 있으면 vulnclass가 그중
	// 하나의 키워드와 일치해야 한다.
	// VulnClassExclude 는 키워드 하나라도 일치하면 제외한다(제외가 포함보다
	// 우선한다).
	// 매칭 방식은 대소문자를 구분하지 않는 부분 문자열이다——정규식보다
	// 안전하다: 사용자가 정규식을 잘못 써도 채널이 조용히 무력화되지 않는다.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange 는 해당 채널이 취약점 상태 변경 이벤트를 받을지 결정한다
	// (realtime 모드에서만 의미가 있다).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter 는 채널 필터 설정을 파싱한다.
//
// **절대 error를 반환하지 않는다.** 이는 의도적인 설계 선택이다: 필터 조건
// 설정이 잘못되면 항상 제로값 Filter로 퇴화한다(= 필터링하지 않음 = 전부
// 통과). 취약점 알림 시스템에서는 **한 건 더 보내는 것이 심각한 취약점 하나를
// 조용히 놓치는 것보다 훨씬 낫기** 때문이다. 파싱 실패를 "전송 안 함"으로
// 만들면, 겉보기에는 설정이 끝난 것 같지만 실제로는 아무것도 보내지 않는
// 채널을 사용자에게 안겨주는 셈이다——이것이 최악의 실패 양상이다.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// 파싱에 실패하면 f는 제로값으로 남으며, 곧 필터링하지 않음을 뜻한다.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity 는 s가 유효한 등급 기준인지 보고한다(빈 문자열은 기준 없음을
// 뜻한다).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate 는 필터 설정에서 **값이 제한된** 필드를 검증하며, 채널을 저장할 때
// 호출된다.
//
// 왜 쓰기 시점에 막아야 하는가: Match는 알 수 없는 기준에 대해 `rank >= 0`으로
// 판단하는데 이는 항상 참이다——즉 min_severity에 오타 하나("hgih")가 있으면
// 필터가 **조용히 무력화**되어 "전부 전송"이 된다. 이는 본 패키지의 "놓치기보다
// 더 보내는" 방향과 일치하지만(놓치지는 않는다), 그 대가로 사용자는 자신이
// 등급별로 분류 전송을 하고 있다고 믿으면서 실제로는 모든 취약점을 그룹에
// 쏟아붓게 되고, 설정이 잘못됐다는 어떤 징후도 없다. 이런 "조용한 성능 저하"는
// 바로 입구에서 막아야 한다.
//
// Validate는 **쓰기** 경로에만 쓰인다. 읽기 경로는 여전히 ParseFilter의
// 관대한 동작을 따르며, 이렇게 해야 이미 저장된 과거의 잘못된 값 때문에
// 채널 전체를 읽지 못하는 일이 없다.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("최저 등급 %q 가 올바르지 않습니다. 선택 가능: low / medium / high / critical, 비워두면 제한 없음", f.MinSeverity)
	}
	return nil
}

// Match 는 어떤 이벤트가 이 필터 조건을 가진 채널로 전달되어야 하는지 판단한다.
//
// ParseFilter와 같은 이유로 **절대 error를 반환하지 않는다**: 내부 이상은
// 모두 "통과"로 처리한다. 판단 순서: 이벤트 유형 → 등급 기준 → 작업/자산
// 범위 → 취약점 유형 키워드.
func Match(f Filter, s Snapshot) bool {
	// 상태 변경 이벤트는 명시적으로 켠 채널만 받는다. 기본값은 꺼짐이다.
	// 대다수 사용자는 "전송"이 "새 취약점 발견"을 뜻한다고 기대하며, 모든
	// 상태 전환을 일일이 따라가며 알려주는 것을 기대하지 않기 때문이다.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// 제외가 우선: 제외 키워드 하나라도 걸리면 탈락, 포함 목록에 동시에
	// 걸려도 마찬가지다.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// 집합이 작으니 선형 스캔으로 충분하다; 양쪽 규모 모두 "사람이 손으로
	// 고른 수십 개" 수준이라, map을 만드는 비용이 이득보다 크다.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold 는 s가 keywords 중 하나라도 포함하는지 보고한다(대소문자
// 구분 없음).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
