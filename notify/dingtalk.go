package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel 은 딩톡(DingTalk) 커스텀 봇을 구현한다.
//
// 플랫폼 특성(여기서의 구현 선택을 좌우한다):
//   - 봇 하나당 분당 20건으로 제한되며, 초과 전송은 조용히 버려진다(HTTP는
//     여전히 200을 반환할 수 있다), 그래서 제한은 클라이언트 쪽에서 반드시
//     해야 한다. DefaultRatePerMin 참고.
//   - 보안 설정은 셋 중 하나: 서명 / 커스텀 키워드 / IP 화이트리스트. 서명은
//     메시지 내용에 의존하지 않는 유일한 방식이라, 서명만 지원한다(셋 다
//     켜지 않은 순수 webhook도 지원).
//   - 성공/실패 모두 HTTP 200을 반환하며, body의 errcode로 구분한다——errcode를
//     확인하지 않으면 전달 실패를 성공으로 기록하게 된다.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// 딩톡의 Webhook 주소에는 접근 토큰이 들어 있어 그 자체로 자격증명이므로, 전체를 마스킹한다.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// 목표는 딩톡 Webhook 주소 자체다; 주소를 바꾸면 새 주소에 대해 서명 키도
// 함께 다시 표명해야 한다.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소가 없습니다")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 주소가 올바르지 않습니다: %w", err)
	}
	return nil
}

// Send 는 메시지를 한 번 전달한다. 상세 링크가 있고 단건이면 ActionCard(버튼 포함)를
// 쓰고, 그렇지 않으면 markdown을 쓴다.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// 딩톡 markdown 본문에는 명확한 바이트 상한이 없지만, 그래도 증거 필드가
	// 비정상적으로 부풀지 않도록 상한 보호를 둔다.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "자세히 보기",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// 딩톡은 업무 에러를 200 응답 안에 숨긴다.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("딩톡 응답 파싱 실패: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000은 서명 검증 실패, 310000은 키워드 불일치——둘 다 설정 오류이며,
		// 재시도로는 저절로 고쳐지지 않는다.
		return 0, Permanent(fmt.Errorf("딩톡이 오류를 반환함 %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL 는 공식 서명 규칙에 따라 webhook에 timestamp와 sign 파라미터를 덧붙인다.
//
// 규칙: 서명할 문자열 = timestamp + "\n" + secret, HMAC-SHA256의 **키도 secret**이며,
// 결과를 base64로 인코딩한 뒤 URL 인코딩한다. timestamp는 밀리초 단위다. secret이(가) 비어 있으면 그대로 반환해,
// 서명을 켜지 않은 봇도 지원한다.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// err를 그대로 전달하지 않는다: url.Parse의 에러 문구에는 전체 주소(access_token
		// 포함)가 들어 있다.
		return "", fmt.Errorf("Webhook 주소 파싱 실패: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL 은 주소가 유효하고 프로토콜이 지원되는지 검증하며, 리터럴 IP
// 목적지에 대해 내부망 여부도 판단한다.
//
// 신경 쓴 두 가지:
//
//  1. **에러 메시지는 반드시 비식별화해야 한다**. url.Parse가 자체로 반환하는
//     *url.Error는 Error()에 **전체 원본 주소**를 담는데, 이 기능이 다루는
//     주소들에는 자격증명이 박혀 있다(딩톡의 접근 토큰, 기업 위챗의 key,
//     Telegram의 bot token, 페이시의 hook id). 예전에는 여기서 바로 `return err`를
//     했는데, 그 결과 "주소 형식이 올바르지 않음" 에러가 자격증명을 그대로
//     실어 날라, 테스트 API의 400 응답, 매 전달마다 저장되는 last_error, 서버
//     로그, 전달 기록 API로 흘러들어갔다.
//
//  2. **리터럴 IP는 바로 내부망으로 판단**하고, 도메인은 접속 단계에서 판단하도록
//     남겨둔다(blockInternalDial이 최종적으로 실효를 갖는 지점이며, DNS 리바인딩도
//     커버한다). 여기서 한 번 더 검사하는 것은 설정을 저장하는 시점에 바로
//     알려주기 위함이며, 첫 전달 실패를 기다리지 않게 한다.
//
// 프로토콜을 제한하는 것은 방어적 조치다: file:// 나 gopher:// 같은 것은
// http.Client가 예상치 못한 동작을 하게 만든다(scheme 검사로 이미 막혀 있지만,
// 이 면을 굳이 열어둘 이유가 없다).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("주소를 파싱할 수 없습니다 (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http/https만 지원합니다, 받은 값 %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("호스트명이 없습니다")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("로컬/링크 로컬 주소 %s 로의 전달은 거부됩니다 (로컬 서비스로 전달이 필요하면 %s=1 설정)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
