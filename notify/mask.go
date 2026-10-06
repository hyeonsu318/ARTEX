package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix 는 마스킹된 값을 나타내는 접두사다. API가 자격증명을 응답으로
// 돌려줄 때 이 접두사가 붙은 값으로 실제 내용을 대체하며, 업데이트 요청에서
// 이 접두사가 붙은 값을 받으면 "DB의 원래 값을 그대로 유지하라"는 뜻으로
// 이해한다.
//
// 빈 문자열이나 고정된 상수 대신 접두사를 쓰는 이유는, 식별에 도움이 되는
// 정보를 약간 덧붙일 수 있기 때문이다(MaskedValue 참고). 그래야 사용자가
// 키를 다시 붙여넣지 않고도 "이게 어느 봇인지" 구분할 수 있다.
const MaskedPrefix = "__masked__"

// MaskedValue 는 마스킹된 값을 생성한다:
//
//	"__masked__"              원래 값이 너무 짧아 아무 힌트도 주지 않음
//	"__masked__:…ab12cd"      원래 값의 끝 6자리를 식별용 힌트로 붙임
//
// 끝 6자리만 노출하는 것은 의도적인 선택이다: Webhook 주소의 식별 정보는
// 끝부분에 있고(예: 기업 위챗의 key, 페이시의 봇 id), 접두사 부분은 모든
// 봇이 동일해서 식별 가치가 없다. 끝 6자리로는 자격증명을 복원할 수 없지만,
// 설정하는 사람이 "이게 내가 아는 그 그룹"이라고 알아보기에는 충분하다.
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked 는 어떤 값이 마스킹된 값인지(즉 API 응답 이후 수정되지 않았는지)
// 보고한다.
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig 는 설정의 복사본을 반환하며, 해당 채널의 자격증명 필드를
// 마스킹된 값으로 바꾼다.
//
// 알 수 없는 채널 유형은 원래 설정이 아니라 빈 map을 반환한다——채널 유형을
// 식별할 수 없을 때는 자격증명이 들어 있을 수 있는 원본 내용을 통째로
// 돌려주기보다, UI에 "설정을 사용할 수 없음"이라고 표시하는 쪽을 택한다.
// 자격증명이 아닌 필드는 그대로 유지해, UI가 정상적으로 표시될 수 있게 한다.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// headers 같은 중첩 구조는 전체를 하나의 자격증명으로 취급한다: 하위
		// 키 하나씩 판단하려면 채널마다 "어떤 하위 키가 자격증명인지"에 대한
		// 규칙을 또 하나 선언해야 하는데, 복잡도가 이득을 훨씬 넘어선다.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials 는 "목적지 주소가 바뀌었는데 호출자가
// 자격증명 필드를 표명하지 않았음"을 나타낸다. 조용히 통과시키거나 자격증명을
// 조용히 버리는 대신 이것을 반환하는 이유는 PrepareConfigUpdate 참고.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // 변경된 목적지 키
	Missing []string // 명시적으로 표명되지 않은 자격증명 키
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "목적지 주소(" + strings.Join(e.Changed, ", ") + ")가 변경되었습니다. 자격증명 필드(" +
		strings.Join(e.Missing, ", ") + ")도 함께 다시 입력하세요: 새 값을 입력하거나, 자격증명이 더 이상 필요 없으면 명시적으로 비워 두세요. " +
		"기존 자격증명은 이전 주소에서만 유효하며, 그대로 유지하면 새 주소에 넘겨주는 셈입니다."
}

// PrepareConfigUpdate 는 채널 설정을 병합하며, "목적지 주소 변경"이라는 보안상
// 민감한 상황을 처리한다.
//
// 채널 업데이트 경로에서 단순한 MergeConfig를 대체하며, 실제로 재현 가능했던
// 다음 경로를 해결한다: 목적지 주소(메시지를 어디로 보낼지)와 자격증명(어떤
// 신원으로 보낼지)은 서로 독립된 필드인데, MergeConfig는 "언급되지 않은 키"를
// 항상 DB의 원래 값으로 유지한다. 그래서 채널을 PATCH할 수 있는 누구든
// **주소만 바꾸고 자격증명은 언급하지 않으면**, 서버가 DB에 저장된 실제
// 자격증명을 의도치 않은 엔드포인트로 보내게 만들 수 있다:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → 기존 Authorization 헤더가 요청과 함께 전송됨
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<실제 Token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → STARTTLS 후 사용자명과 비밀번호를 넘김
//
// 이 경로는 완전히 조용하고 리다이렉트에도 의존하지 않으며(그래서 호스트 간
// 이동 거부로는 막을 수 없다), 본 패키지 마스킹 메커니즘의 목표인 "자격증명을
// 브라우저에 노출하지 않음"을 정면으로 무너뜨린다.
//
// 규칙: 어떤 목적지 키가 새 값으로 바뀌면, 호출자는 **모든** 자격증명 키에
// 대해 명시적으로 표명해야 한다:
//   - 새 값을 제공 → 새 값을 사용
//   - 명시적으로 빈 문자열 전달 → 해당 필드는 더 이상 자격증명이 필요 없음(비움 의미 유지)
//   - 마스킹된 값을 그대로 되돌려 보냄 / 해당 키를 아예 언급하지 않음 → 거부
//
// 세 번째 경우를 거부하는 이유는 "마스킹된 값"의 의미가 바로 "기존 자격증명을
// 계속 사용"이며, 기존 자격증명은 이전 주소에서만 유효하기 때문이다. 여기서
// "자격증명 자동 폐기"를 일부러 하지 않는 것은, 선택적 자격증명 필드(webhook의
// headers, email의 password)에서는 그것이 "인증이 사라졌는데 API는 200을
// 반환"하는 상태로 조용히 바뀌어, 에러보다 원인 추적이 더 어렵기 때문이다.
// 작업자가 한 번 더 입력하게 하는 편이 낫다.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("채널 유형 %q 이(가) 등록되지 않았습니다", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// 문자열이 아닌 자격증명 값(예: webhook의 headers는 객체) 안에 마스킹
	// 리터럴이 박혀 있다면, 호출자가 "원래 값 유지"용 표식을 구조체 내부에
	// 밀어 넣었다는 뜻이다. MergeConfig는 "문자열이면서 접두사가 붙은 것"만
	// 마스킹으로 인식하므로, 이런 형태는 일반 객체로 취급되어 그대로 저장된다
	// ——DB에 리터럴 "__masked__"가 실제로 기록되고, 이후 인증이 조용히
	// 무효화되는데 어떤 에러도 나지 않는다. 거부하는 편이 낫다.
	//
	// 이 검사는 반드시 **맨 앞에** 두어야 한다: 주소가 바뀌지 않았을 때는
	// 조기 반환 경로로 가므로, 뒤에 두면 "주소 변경" 경로 하나만 커버하게
	// 된다(첫 버전이 바로 이렇게 잘못 배치되었고, 테스트가 곧바로 잡아냈다).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// 실제로 바뀐 목적지 키를 찾는다. 마스킹된 값은 "안 바뀜"과 같다.
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// 주소가 바뀌지 않았으면 일반 병합을 한다(마스킹된 값은 원래 값 유지,
		// 빈 문자열은 비움, 나머지는 덮어쓰기).
		return MergeConfig(stored, incoming), nil
	}

	// 주소가 바뀜: 모든 자격증명 키에 대해 명시적 표명을 요구한다.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers 는 마스킹 표식을 문자열이 아닌 구조 안에 넣어
// 제출하는 것을 거부한다.
//
// 마스킹 메커니즘의 전제는 "값 전체가 문자열"이라는 것이다. webhook의 headers
// 같은 객체 필드는 전체를 마스킹하거나(문자열 "__masked__"로 기록) 전체를
// 제출해야 한다; 표식을 객체 내부에 끼워 넣으면 "변경 없음"을 표현하지도
// 못하고, 실제 값으로 취급되어 DB에 저장된다.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("필드 %s 의 내용에 마스킹 표식 %q 이(가) 들어 있습니다: 이 필드는 전체를 비워 기존 값 유지를 표시하거나 전체를 새 값으로 제출해야 하며, 구조체 내부에 마스킹 자리표시자를 끼워 넣을 수 없습니다",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue 는 두 설정 값이 동등한지 비교한다. JSON 직렬화로 비교하는
// 것은 타입 차이를 함께 처리하기 위해서다——프런트가 제출한 포트는 number인데
// DB에서 읽어온 것은 float64라서, 그냥 ==로 비교하면 잘못 판정한다.
//
// "빈 값"은 먼저 정규화한 뒤 비교해야 한다: 이 설정 모델에서 빈 문자열과
// "키가 없음"은 같은 상태이다. MergeConfig가 빈 문자열을 명시적 비움으로
// 취급해 해당 키를 그대로 delete하기 때문이다. 정규화하지 않으면, 항상 비워
// 두는 선택적 목적지 필드(Telegram의 base_url이 유일하게 그런 필드다: 비우면
// 공식 주소를 쓴다)가 다음 경로를 타게 된다——
//
//	새로 만들 때 base_url:""을 저장  →  첫 저장에서 MergeConfig가 키를 삭제
//	→ 두 번째 저장 시 incoming은 "", stored에는 키가 없어 "주소가 바뀜"으로 판정
//	→ 자격증명은 마스킹된 값 → 400 "목적지 주소가 변경되었습니다. 자격증명 필드도 함께 다시 입력하세요"
//
// 이후 저장할 때마다 실패하며, 사용자가 Bot Token을 다시 붙여넣지 않는 한
// 해결되지 않는데, 정작 사용자는 아무것도 바꾸지 않았다.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue 는 설정 값이 "비어 있음"인지 판정한다.
// 기준은 MergeConfig의 비움 판정(strings.TrimSpace(s) == "")과 일치해야
// 한다. 그러지 않으면 "MergeConfig는 삭제해야 한다고 보는데 sameConfigValue는
// 값이 있다고 보는" 틈이 생긴다.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig 는 incoming을 stored 위에 병합하며, 채널 설정을 업데이트할 때
// 쓴다.
//
// 규칙:
//   - incoming에서 값이 마스킹인 키 → stored의 원래 값 유지(사용자가 이
//     필드를 바꾸지 않음)
//   - incoming에서 값이 빈 문자열인 키 → 명시적 비움으로 보고 해당 키 삭제
//   - 나머지 키 → incoming의 값으로 덮어씀
//   - stored에는 있고 incoming에는 없는 키 → 유지(부분 업데이트 의미)
//
// 빈 문자열을 "비움"으로 칠지는 명확히 해야 한다: 프런트 폼은 채우지 않은
// 필드를 빈 문자열로 제출하는데, 이를 유효한 값으로 기록하면 "비워 두어 원래
// 값을 유지"하려던 필드가 실제로 지워진다. 여기서는 명시적 비움을 택했다.
// 잘못 설정된 필드를 지우려 할 때 사용자에게 다른 표현 수단이 없기
// 때문이다(필드를 빼면 "제공 안 함"과 "빈 값 제공"을 구분할 수 있지만, UI는
// 이 구분을 쓰지 않는다).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // 마스킹된 값 = 수정 안 함, stored 유지
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
