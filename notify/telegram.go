package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit 은 Telegram sendMessage의 text 필드 상한이다(문자 수).
const telegramTextLimit = 4096

// telegramChannel 은 Telegram Bot API를 구현한다.
//
// 플랫폼 특성:
//   - 인증이 전부 URL path에 들어 있어(/bot<token>/sendMessage) 서명이
//     필요 없다.
//   - MarkdownV2 대신 HTML 파싱 모드를 쓴다: MarkdownV2는 `_*[]()~`>#+-=|{}.!`
//     총 18개 문자를 이스케이프해야 하고 하나만 빠뜨려도 메시지 전체가 거부되지만,
//     HTML은 & < > 세 개만 이스케이프하면 된다.
//   - 업무 에러도 마찬가지로 HTTP 200 안에 숨어 있으며, ok 필드로 판단한다.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram은 1:1 대화에서 초당 약 1건, 그룹에서는 분당 20건이다. 보수적인 값을 쓴다.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token 이 자격증명 전체다; chat_id는 수신자일 뿐 비밀이 아니다(Token 없이는
// chat_id만으로 메시지를 보낼 수 없다).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url은 Token이 어느 API 엔드포인트로 전송될지 결정한다(자체 구축 리버스
// 프록시 등). 이를 바꾸면 Token도 반드시 다시 표명해야 한다.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("Bot Token이 없습니다")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("Chat ID가 없습니다")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("API 주소가 올바르지 않습니다: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Telegram 응답 파싱 실패: %w (%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429는 제한 초과이며 백오프 후 재시도가 효과 있다; 나머지(400 파라미터 오류,
	// 401 token 오류, 403 차단됨, 404 chat 없음)는 모두 설정 문제이며 재시도로는
	// 저절로 고쳐지지 않는다.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram 제한 초과: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram이 오류를 반환함 %d: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint 는 sendMessage 주소를 만든다. base_url이 비어 있으면 공식
// API를 쓰고, 값이 있으면 자체 구축 Bot API 리버스 프록시(중국 내 네트워크
// 환경에서 흔한 요구사항)에 쓴다.
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// err를 그대로 전달하지 않는다: 주소에 Bot Token이 들어 있어서, 이 시점엔
		// addr조차 그대로 노출하면 안 된다.
		return "", fmt.Errorf("API 주소 조합 실패 (API 주소: %s)", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML 은 HTML 본문을 렌더링하며, 본문과 실제로 쓰인 항목 수를
// 반환한다(Channel.Send 참고).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Telegram의 상한은 **문자 수**이므로, 묶는 것도 문자 단위로 계량한다(runeSize).
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">플랫폼에서 전체 보기</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b>상태 변경</b>：%s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b>유형</b>：" + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b>자산</b>：" + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b>요약</b>：" + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">자세히 보기</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes 는 메시지 제목과 발생할 수 있는 절단 안내문에 남겨둘
// 여유분이다(문자 수 기준).
const telegramReservedRunes = 160

// telegramBatchLine 은 요약 안의 한 줄을 렌더링한다(이스케이프하지 않으며, 호출자가
// 일괄 이스케이프한다).
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle 은 요약 메시지의 제목 줄을 렌더링한다. 건수는 **이 메시지에
// 실제로 담긴** 건수를 쓰며, 전체 배치 건수가 아니다——그러지 않으면 읽는 사람이
// 메시지 머리에 쓰인 숫자를 전체로 오해한다.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("취약점 요약 · 총 %d건", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf("(앞 %d건 표시, 나머지 %d건은 다음 메시지에서 계속)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("최근 %d분 · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape 는 HTML 텍스트 내용을 이스케이프한다.
// Telegram은 이 세 가지 엔터티만 인식하며, 이스케이프하면 이미 있던 &amp; 같은
// 엔터티도 다시 이스케이프된다——이것이 의도된 동작이다: 우리가 보여주려는 것은
// 원본 문자이며, 사용자가 HTML을 주입하게 해서는 안 된다.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr 는 HTML 속성 값을 이스케이프한다. 텍스트 이스케이프 외에
// 인용부호도 처리해야 한다——URL에 인용부호가 있으면 href 속성이 조기에 닫혀
// 뒤의 내용이 주입 지점이 될 수 있다.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
