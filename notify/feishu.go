package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel 은 페이시(Feishu, Lark 포함) 커스텀 봇을 구현하며, 인터랙티브
// 카드 방식을 쓴다.
//
// 플랫폼 특성:
//   - 서명 알고리즘이 딩톡과 **다르며**, 매우 틀리기 쉽다. feishuSign 주석 참고.
//   - 딩톡과 마찬가지로 업무 에러를 HTTP 200의 body 안에 넣는다(code != 0).
//   - 카드 header는 색상 템플릿을 지원해, 등급을 색으로 매핑하면 메시지 목록에서
//     한눈에 심각도를 알아볼 수 있다.
type feishuChannel struct{}

func (feishuChannel) Kind() string { return KindFeishu }

// 페이시 커스텀 봇은 초당 약 5회, 분당 100회에 해당한다.
func (feishuChannel) DefaultRatePerMin() int { return 100 }

// Webhook 주소 끝부분이 곧 봇의 고유 식별자이며, 자격증명에 해당한다.
func (feishuChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// 마찬가지로: Webhook 주소를 바꾸면 새 주소에 대해 서명 키도 다시 표명해야 한다.
func (feishuChannel) DestinationKeys() []string { return []string{"webhook"} }

func (feishuChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소가 없습니다")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 주소가 올바르지 않습니다: %w", err)
	}
	return nil
}

func (c feishuChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	card, kept := feishuCard(m)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}
	// 서명 파라미터는 메시지와 같은 레벨에 놓이며, 설정된 secret 이 있을 때만 나타난다.
	if secret := cfgString(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		// 일부 버전의 페이시 hook은 이 필드명을 쓰므로 함께 호환한다.
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("페이시 응답 파싱 실패: %w (%s)", err, snippet(raw))
	}
	if res.Code != 0 {
		return 0, Permanent(fmt.Errorf("페이시가 오류를 반환함 %d: %s", res.Code, res.Msg))
	}
	if res.StatusCode != 0 {
		return 0, Permanent(fmt.Errorf("페이시가 오류를 반환함 %d: %s", res.StatusCode, res.StatusMessage))
	}
	return kept, nil
}

// feishuSign 은 페이시 공식 규칙에 따라 서명을 계산한다.
//
// 여기서 특히 틀리기 쉽다: 공식 예시는
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// 즉 **key = timestamp + "\n" + secret, message는 빈 값**이며, 직관적으로 떠올리기 쉬운
// 「key=secret, message=stringToSign」이 아니다——그건 바로 딩톡의 알고리즘이다. 두 쪽의 알고리즘이 정확히
// 뒤바뀌어 있어서, 다른 쪽 구현을 그대로 따라 쓰면 반드시 서명 검증이 실패한다(19021 오류).
func feishuSign(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate 는 취약점 등급을 카드 header 색상 템플릿으로 매핑한다.
// 알 수 없는 등급은 grey를 쓴다——blue를 쓰지 않는 이유는 low와 혼동하지 않기 위해서다.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes 는 카드 콘텐츠의 보수적인 상한이다. 페이시는 카드 크기에
// 제한을 두며, 넘으면 메시지 전체가 거부된다; 공식 상한보다 명확히 낮은 값을
// 택해, JSON 포장에 드는 오버헤드까지 감안했다.
const feishuMaxCardBytes = 24000

// feishuCard 는 인터랙티브 카드를 구성해 카드와 **실제로 담긴 항목 수**를 반환한다.
// kept의 용도는 markdownBody와 같다: 카드에 실제로 들어간 항목만 전달 완료로
// 표시해야 한다.
func feishuCard(m Message) (map[string]any, int) {
	elements := []any{}
	kept := 0
	if m.Batch {
		// 머리글을 붙이기 전에 먼저 건 단위로 담을 양을 계산한다: 머리글에
		// "나머지 N건은 다음 메시지에서 계속"이라고 써야 하는데, N은 실제로
		// 담긴 건수에서 나와야 한다.
		kept = packItemCount(m.Items, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, idx int) string {
			return feishuBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		elements = append(elements, feishuMarkdownDiv(markdownBatchIntro(m, items, len(m.Items))))
		for i, it := range items {
			elements = append(elements, feishuMarkdownDiv(feishuBatchLine(it, i+1)))
		}
		if m.HomeURL != "" {
			elements = append(elements, feishuButton("플랫폼에서 전체 보기", m.HomeURL))
		}
	} else if len(m.Items) > 0 {
		kept = 1
		it := m.Items[0]
		elements = append(elements, feishuMarkdownDiv(feishuItemLines(it)))
		if it.DetailURL != "" {
			elements = append(elements, feishuButton("자세히 보기", it.DetailURL))
		}
	}

	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(m)}},
		"elements": elements,
	}
	if len(m.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(m.Items[0].Severity)
	}
	return card, kept
}

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url":  url,
			"type": "primary",
		}},
	}
}

// feishuItemLines 는 취약점 한 건의 lark_md 본문을 렌더링한다.
//
// lark_md는 markdown과 같은 계열의 텍스트 형식이며 링크와 강조를 똑같이
// 해석하므로, 외부에서 온 필드는 모두 markdownText(한 줄로 압축 + 이스케이프)를
// 거쳐야 한다——그러지 않으면 취약점 제목 하나가 페이시에서 클릭 가능한
// 외부 링크로 바뀔 수 있다.
func feishuItemLines(it Item) string {
	out := fmt.Sprintf("**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if it.IsStatusChange() {
		out += fmt.Sprintf("\n**상태 변경**：%s → %s",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		out += fmt.Sprintf("\n**유형**：%s", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		out += fmt.Sprintf("\n**자산**：%s", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			out += fmt.Sprintf("\n**요약**：%s", s)
		}
	}
	return out
}

// feishuBatchLine 는 요약 카드 안의 한 줄을 렌더링한다.
func feishuBatchLine(it Item, index int) string {
	line := fmt.Sprintf("**%d. %s · %s**", index, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		line += " — " + markdownText(a, 0)
	}
	return line
}
