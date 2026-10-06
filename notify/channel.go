package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel 은 알림 채널 하나의 어댑터다. 구현체는 무상태여야 한다: 같은 인스턴스가
// 여러 채널 설정에서 동시에 재사용되므로, 자격증명은 항상 cfg 파라미터로 전달받는다.
type Channel interface {
	// Kind 는 채널 유형 식별자를 반환하며, 레지스트리의 키와 일치해야 한다.
	Kind() string
	// Validate 는 설정을 저장할 때 호출되어 필수 필드와 형식을 검증한다. 반환된
	// 에러는 설정하는 사람에게 그대로 노출되므로, 문구는 어느 필드가 빠졌는지를
	// 말해야 하며 뭉뚱그려 설정이 올바르지 않다고 해서는 안 된다.
	Validate(cfg map[string]any) error
	// Send 는 메시지 한 번을 전달하고, 실제로 전달된 항목 수와 에러를 반환한다.
	//
	// 왜 건수를 반환해야 하는가: 각 플랫폼마다 메시지 길이 상한이 있어서, 요약
	// 메시지가 한 묶음을 전부 담지 못하면 잘려 나간다. 호출자가 무조건 전체
	// 묶음을 전달 완료로 표시해 버리면 잘려 나간 항목들은 그대로 사라진다——
	// 메시지에도 보이지 않고 전달 기록에는 성공으로 표시되어, 어디서도 그
	// 취약점이 전송되지 않았다는 것을 알 수 없다. kept를 반환하면 호출자는
	// 앞의 kept개만 전달 완료로 처리하고 나머지는 다음 배치로 남긴다.
	//
	// 에러를 반환하면 전달 실패를 의미하며, 그중 *PermanentError 는 재시도하지
	// 말아야 함을 뜻한다. 실패 시 kept 값은 의미가 없으므로 호출자는 무시해야 한다.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin 은 해당 채널이 공식적으로 권장하는 분당 전달 상한을
	// 반환하며, 새 채널 인스턴스를 만들 때 기본 제한값으로 쓰인다. 0을 반환하면
	// 알려진 제한이 없다는 뜻이다.
	DefaultRatePerMin() int
	// SecretKeys 는 해당 채널 설정 중 자격증명에 해당하는 키 이름을 반환한다. API 응답에서 이 키들의 값은 마스킹되며,
	// 업데이트 시 마스킹된 값을 받으면 DB에 저장된 원래 값을 그대로 유지한다. 어떤 필드가 자격증명인지는
	// (기업 위챗은 Webhook 주소 전체가 자격증명이지만, 딩톡은 그중secret），
	// 이 지식은 채널이 제공해야 하며 상위 계층이 추측해서는 안 된다.
	SecretKeys() []string
	// DestinationKeys 는 해당 채널 설정에서 메시지를 어디로 보낼지를 결정하는
	// 키 이름을 반환한다.
	//
	//SecretKeys 와 마찬가지로 보안과 관련된 요소다: 목적지 주소와 자격증명은 서로 독립된 필드 집합이므로,
	// 주소만 바꾸고 자격증명은 그대로 두는 것을 허용하면, 채널 설정을 변경할 수
	// 있는 누구든 DB에 저장된 원래 자격증명이 의도치 않은 곳으로 전달되게 만들 수
	// 있어, 채널 설정 마스킹이 완전히 무의미해진다.
	// 자세한 내용은 PrepareConfigUpdate 참고.
	DestinationKeys() []string
}

// registry 는 채널 레지스트리다. init()으로 자동 등록하지 않고 명시적 리터럴을
// 쓴 것은 의도적이다: 이렇게 하면 "어떤 채널이 있는지"를 한곳에서 전부 볼 수 있고,
// 새 채널을 추가했을 때 빠뜨린 부분이 런타임 부작용이 아니라 컴파일 시점에 드러난다.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get 은 유형으로 채널 구현체를 가져온다.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind 는 kind가 지원되는 채널 유형인지 보고한다.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds 는 지원되는 모든 채널 유형을 사전순으로 반환한다(UI 드롭다운에서
// 안정적으로 표시하기 위함).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError 는 재시도해서는 안 되는 전달 실패를 표시한다: 자격증명 오류,
// 목적지의 거부, 잘못된 요청 본문 등. 재시도는 일시적 장애(네트워크 지연,
// 레이트리밋, 상대측 5xx)에만 의미가 있다; 영구 실패를 반복해서 백오프
// 재시도하면 성공하지도 못하면서 실제 에러를 재시도 로그 속에 묻어버린다.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent 는 err를 영구 실패로 표시한다. err가 nil이면 nil을 반환해
// `return Permanent(someCheck())` 형태로 쓸 수 있게 한다.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent 는 err 체인에 영구 실패 표시가 있는지 보고한다.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- 설정 읽기 helper ----
//
// 채널 설정은 데이터베이스의 JSONB 컬럼에서 오며, encoding/json으로 역직렬화하면
// map[string]any가 되고 숫자는 모두 float64, 배열은 []any가 된다. 아래 helper들은
// 이 변환 과정을 일관되게 처리하며, 사용자가 UI에서 빈 칸으로 두어 생기는 타입
// 차이(예: 포트를 문자열로 입력한 경우)도 허용한다.

// cfgString 은 문자열 설정 값을 가져오며, 앞뒤 공백을 항상 제거한다——웹 폼에서
// 복사해 붙이면 공백이 섞여 들어오기 쉽다.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt 는 정수 설정 값을 가져오며, float64(JSON 기본값)와 문자열 두 가지
// 형식을 모두 허용한다.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool 은 불리언 설정 값을 가져오며, 문자열 "true"/"1"도 허용한다.
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings 는 문자열 배열 설정 값을 가져오며, 공백을 자동으로 잘라내고
// 빈 문자열은 버린다.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// 단일 문자열도 허용해, 값이 하나뿐인 폼 제출을 편하게 한다.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap 은 문자열 맵 설정 값(커스텀 HTTP 헤더 등)을 가져오며, 키와 값의
// 공백을 모두 제거하고 빈 키는 버린다.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
