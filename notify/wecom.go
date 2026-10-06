package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit 는 기업 위챗 그룹 봇 markdown content의 하드 상한이다(바이트,
// 문자가 아니다). 여섯 채널 중 가장 빡빡한 제한이며, TruncateBytes가 존재하는
// 주된 이유이기도 하다.
const weComMarkdownLimit = 4096

// weComChannel 은 기업 위챗 그룹 봇을 구현한다.
//
// 플랫폼 특성:
//   - URL 상의 key로만 인증하며 서명을 지원하지 않는다——그래서 webhook 주소
//     자체가 자격증명 전부다.
//   - markdown content 상한은 4096 **바이트**이며, 넘으면 메시지 전체가 거부된다
//     (자르는 것이 아니다). 한중일 문자는 3바이트/자이므로, 본문에 천몇백 자밖에
//     쓸 수 없다는 뜻이라 클라이언트에서 반드시 잘라야 한다.
//   - 분당 20건 제한도 마찬가지로 클라이언트 쪽 제한으로 막아야 한다.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// 기업 위챗은 Webhook 하나만 자격증명이며(URL 상의 key), 서명을 지원하지
// 않는다——주소 전체가 자격증명이므로 마스킹할 다른 필드가 없다.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// 기업 위챗은 Webhook 하나뿐인 필드이며, 이것이 목적지이자 자격증명이므로
// "주소를 바꾼 뒤 남는 자격증명" 같은 것은 존재하지 않는다.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소가 없습니다")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 주소가 올바르지 않습니다: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// 요약 배치는 꽤 길 수 있어서(50건 × 건당 한 줄 + 접두사), 4096바이트를 쉽게
	// 넘는다. 자르기는 플랫폼의 거부에 맡기지 않고 여기서 한다: 거부당하면 그
	// 배치 전체를 잃지만, 잘라내면 적어도 앞쪽 몇 건은 전달된다.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("기업 위챗 응답 파싱 실패: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009는 API 호출이 제한을 초과했다는 뜻이다——플랫폼의 제한 윈도우는
		// 흘러가므로, 백오프 후 재시도가 효과가 있어 명시적으로 재시도 가능으로
		// 분류한다. 여기에 도달했다는 것은 클라이언트의 rate_per_min이 너무
		// 공격적으로 설정됐다는 뜻이며, 재시도는 임시방편일 뿐 진짜 해결책은
		// 이 채널의 제한값을 낮추는 것이다.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("기업 위챗 제한 초과 %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000은 webhook key가 올바르지 않다는 뜻이다——영구 실패이며, 재시도로는
		// 저절로 고쳐지지 않는다.
		return 0, Permanent(fmt.Errorf("기업 위챗이 오류를 반환함 %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
