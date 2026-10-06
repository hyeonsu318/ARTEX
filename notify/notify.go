// Package notify 는 취약점 발견 알림의 IM / 이메일 전송 채널 어댑터 계층을 구현한다.
//
// 계층 구조: 본 패키지는 **leaf 패키지**로 표준 라이브러리에만 의존한다. 데이터베이스나
// server를 알지 못한다. 채널 설정은 map[string]any로 전달되고(notification_channels.config
// JSONB 컬럼에 대응), 전송할 내용은 Message로 전달된다. 이렇게 분리한 이유는: 서명 계산,
// UTF-8 절단, 필터 매칭처럼 실제로 버그가 나기 쉬운 부분을 PostgreSQL 없이 단위 테스트할
// 수 있고, 호스트는 server 쪽에서 오케스트레이션만 하면 되기 때문이다.
//
// 동시성 규칙: Channel 구현체는 **무상태**여야 한다. 같은 Channel 인스턴스가 여러 채널
// 설정(심지어 같은 채널의 여러 봇 인스턴스)에서 동시에 재사용되므로, 모든 자격증명은
// 항상 cfg 파라미터로 전달받아야 하며, webhook URL 같은 것을 구현체 자신의 필드에
// 캐시해 두는 것은 허용되지 않는다.
package notify

// 채널 유형 식별자. 이 값들은 동시에 notification_channels.kind의 유효 집합이며,
// server 쪽에서 화이트리스트로 검증한다(findings.status와 마찬가지로 DB CHECK를
// 쓰지 않아, 이후 채널 추가가 쉽다).
const (
	KindDingTalk = "dingtalk" // 딩톡(DingTalk) 커스텀 봇
	KindFeishu   = "feishu"   // 페이시(Feishu/Lark 포함) 커스텀 봇
	KindWeCom    = "wecom"    // 기업 위챗(WeCom) 그룹 봇
	KindWebhook  = "webhook"  // 범용 Webhook: 커스텀 메서드/헤더/JSON 템플릿
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP 이메일
)

// 이벤트 유형, notification_events.kind에 대응한다.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind 는 config의 kind가 빈 값일 때의 기본값이다.
const InitKind = KindDingTalk

// severityRank 는 취약점 등급을 비교 가능한 순서값으로 매핑한다. 알 수 없는 등급은
// 0을 반환하므로, min_severity를 설정하면 알 수 없는 등급은 항상 걸러진다——확신이
// 없으면 보내지 않아, 오탐이 쏟아지는 것을 막는다.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank 는 등급의 순서값을 반환한다; 알 수 없는 등급은 0을 반환한다.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel 은 emoji가 붙은 등급명을 반환하며, 메시지 제목과 카드 색상에
// 쓰인다. 알 수 없는 등급은 그대로 돌려줄 뿐 임의로 만들어내지 않는다.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 심각"
	case "high":
		return "🟠 고위험"
	case "medium":
		return "🟡 중위험"
	case "low":
		return "🔵 저위험"
	default:
		return severity
	}
}

// StatusLabel 은 처리 상태를 표시용 문구로 변환하며, 상태 변경 메시지에 쓰인다.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "대기중"
	case "in_progress":
		return "처리 중"
	case "confirmed":
		return "확인됨"
	case "resolved":
		return "처리됨"
	case "fixed":
		return "수정됨"
	case "false_positive":
		return "오탐"
	case "ignored":
		return "무시됨"
	case "duplicate":
		return "중복"
	case "risk_accepted":
		return "위험 수용"
	default:
		return status
	}
}

// AtLeast 는 severity가 min 기준에 도달하는지 판단한다. min이 비어 있으면
// 기준을 두지 않고 모두 통과시킨다. 알 수 없는 severity의 순서값은 0이어서,
// 비어 있지 않은 min에는 모두 걸러진다(severityRank 주석 참고).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
