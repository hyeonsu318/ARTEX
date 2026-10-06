package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel 은 범용 Webhook 어댑터다: 사용자가 URL, 메서드, 요청 헤더,
// JSON 템플릿을 직접 정한다. 이 덕분에 Slack / Mattermost / Discord / 자체
// 구축 시스템마다 따로 구현을 둘 필요가 없다——이 플랫폼들은 모두 설정 가능한
// 템플릿 하나로 커버된다.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// 범용 Webhook은 공식적인 제한이 없으므로 0을 반환해 기본적으로 제한을 두지
// 않으며, 사용자가 상대측 처리 능력에 맞춰 직접 정하게 한다.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// url과 headers를 마스킹한다: 목적지 주소 자체에 토큰이 들어 있는 경우가
// 많고, 커스텀 헤더에는 보통 인증 자격증명이 들어 있어서, 둘 다 API 응답에
// 노출될 수 있으므로 모두 막아야 한다.
// 그 대가로, 헤더 중 하나만 바꾸고 싶어도 전체 헤더 세트를 다시 입력해야 한다
// (마스킹된 값은 "원래 값 유지"로 해석된다)——이 선택은 의도적이다: 한 번 더
// 입력하는 수고를 감수하더라도, 자격증명이 브라우저로 노출되게 하지 않는다.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// 목적지는 url이다. url을 바꿀 때는 headers도 반드시 다시 표명해야 한다——
// 그러지 않으면 원래의 Authorization 헤더가 새 주소로 그대로 전송되며, 이는
// 마스킹을 우회하는 주된 경로다.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate 은 템플릿을 채우지 않았을 때의 기본 요청 본문이다:
// 단순한 JSON 구조로, "JSON 한 건을 받아 저장"하는 대다수 자체 구축 수신단을
// 커버한다.
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData 는 사용자 템플릿에 노출되는 컨텍스트다.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt 은 이번 전달 시각(RFC3339)이며, 수신단이 기록할 수 있도록 제공한다.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel 은 상태 변경을 읽기 쉽게 설명한 문구로, 예: 「대기중 → 수정됨」;
	// 상태 변경이 아니면 비어 있다.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("목표 URL이 없습니다")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("목표 URL이 올바르지 않습니다: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("지원하지 않는 메서드입니다 %s (GET/POST/PUT/PATCH 사용 가능)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("요청 본문 템플릿 문법 오류: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET은 요청 본문을 쓰지 않는다: 내용을 query에 끼워 넣는 것은 템플릿이 할 수
	// 있는 범위를 넘고 GET의 의미에도 맞지 않으므로, GET은 "트리거되면 훅을 울리는"
	// 유형의 수신단에만 적합하다.
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// 템플릿이 렌더링한 결과는 문자열 형태의 JSON이므로, 이를 json.RawMessage로
		// 변환해 그대로 보낸다. 다시 이스케이프해서 사용자가 정성껏 만든 구조를
		// JSON 문자열 안에 또 가두는 일을 피한다.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("요청 본문 템플릿 렌더링 결과가 올바른 JSON이 아닙니다"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// 덮어쓰기를 허용하지만 headers보다 나중에 적용해, 명시적 설정이
		// 우선하도록 한다.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// 범용 Webhook은 본문을 자르지 않는다(수신단은 사용자 자신의 서비스이고
	// 크기는 body_template이 결정하므로), 따라서 전체 배치가 전달된 것으로 본다.
	return len(m.Items), nil
}

// renderWebhookBody 는 사용자 템플릿(또는 기본 템플릿)으로 요청 본문을 렌더링한다.
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("요청 본문 템플릿 문법 오류: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("요청 본문 템플릿 렌더링 실패: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate 은 템플릿을 파싱한다.
//
// missingkey=zero는 누락된 map 키를 에러 대신 제로값으로 렌더링한다——다만 이
// 파일의 컨텍스트는 구조체이므로, 주된 효과는 .Items가 비어 있을 때 range가
// 에러 나지 않게 하는 것이다. 실제로 막아야 하는 것은 .Items가 nil인 경우다.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs 는 템플릿에 노출되는 헬퍼 함수다.
var webhookTemplateFuncs = template.FuncMap{
	// json 은 임의의 값을 JSON으로 직렬화한다.
	//
	// 이 함수는 있으면 좋은 정도가 아니라 필수다: 없으면 사용자는 {{.Title}}로
	// 바로 끼워 넣을 수밖에 없는데, 취약점 제목에 인용부호나 줄바꿈이 하나만
	// 있어도 요청 본문 전체가 더 이상 올바른 JSON이 아니게 된다——수신단은
	// 거부하고, 에러 메시지는 "JSON 파싱 실패"를 가리킬 뿐 제목에 인용부호가
	// 있다는 것은 전혀 짐작할 수 없다.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons 는 JSON 조각을 다른 JSON 문자열 값 내부에 끼워 넣을 때 쓴다(문자열
	// 이스케이프를 한 겹 적용).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// 바깥쪽 인용부호를 제거한다: 인용부호를 붙일지는 호출하는 쪽에서 정한다.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
