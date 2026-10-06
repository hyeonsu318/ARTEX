package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 이 파일은 불변 조건 테스트다: 채널 구현에서 튀어나오는 **어떤** 에러 문구도
// 자격증명을 담고 있어서는 안 된다.
//
// 왜 별도 파일로 뺐는가: 처음의 채널 테스트 케이스는 성공 경로와 플랫폼 업무
// 에러만 다뤘고, 전송 계층 실패는 전혀 보지 않았다. 그런데 가장 위험한 것이
// 바로 전송 계층 에러(연결 거부/DNS 실패/타임아웃)다——http.Client.Do가
// 반환하는 *url.Error는 에러 문구에 **전체 URL**을 찍는데, 이 기능이 다루는
// 플랫폼들의 자격증명은 URL 안에 들어 있다. 자격증명이 이 문자열을 따라 네
// 곳으로 흘러갔다:
//
//	notification_deliveries.last_error  → 평문으로 DB에 저장
//	GET /api/notify/deliveries 응답     → 채널 설정의 마스킹을 우회해 브라우저에 노출
//	서버 로그                           → 흔히 외부로 반출되어 보관됨
//	테스트 전송 API의 502 응답          → 프런트에 그대로 표시됨
//
// 그래서 여기서는 함수 하나만 테스트하지 않고, 채널마다 반드시 실패하는 요청을
// 실제로 한 번씩 보내서, 에러 문구에서 그 자격증명을 찾을 수 없음을 단언한다.

// credentialCases 는 "자격증명이 URL 안에 있는" 모든 채널 형태를 다룬다:
// 딩톡/기업 위챗은 query, 페이시는 경로 끝부분, Telegram은 경로 중간.
var credentialCases = []struct {
	name   string
	ch     Channel
	cfg    map[string]any
	secret string
}{
	{
		name:   "딩톡 access_token 이 query 에 있음",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send?access_token=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "기업 위챗 key 가 query 에 있음",
		ch:     weComChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/cgi-bin/webhook/send?key=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "페이시 hook id 가 경로 끝부분에 있음",
		ch:     feishuChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/open-apis/bot/v2/hook/" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "Telegram bot token 이 경로 중간에 있음",
		ch:     telegramChannel{},
		cfg:    map[string]any{"bot_token": leakProbeToken, "chat_id": "1", "base_url": "http://127.0.0.1:1"},
		secret: leakProbeToken,
	},
	{
		name:   "딩톡 서명 키",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send", "secret": leakProbeToken},
		secret: leakProbeToken,
	},
}

// leakProbeToken 은 실제 자격증명일 수 없는 센티널 값이며, 에러 문구에서 이를
// 검색하는 데 쓴다.
const leakProbeToken = "LEAKPROBE0123456789abcdef"

// TestChannelErrorsNeverLeakCredentials 는 핵심 불변 조건이다.
func TestChannelErrorsNeverLeakCredentials(t *testing.T) {
	for _, tc := range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			// 반드시 실패하는 상대: 127.0.0.1:1은 아무도 수신하지 않으므로, 연결
			// 거부 경로를 탄다.
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{
				Items: []Item{{FindingID: 1, Severity: "high", Name: "유출 탐지용"}},
			})
			if err == nil {
				t.Fatal("도달할 수 없는 주소에는 에러가 나야 한다")
			}
			assertNoSecret(t, err.Error(), tc.secret)
		})
	}
}

// TestChannelErrorsNeverLeakCredentialsInPermanentPath 는 영구 실패 분기를 다룬다:
// URL 검증 실패, 플랫폼 업무 에러 등도 에러 문구를 밖으로 전달하므로, 마찬가지로
// 자격증명을 담고 있어서는 안 된다.
func TestChannelErrorsNeverLeakCredentialsInPermanentPath(t *testing.T) {
	cases := []struct {
		name string
		ch   Channel
		cfg  map[string]any
	}{
		// 주소에 자격증명이 있지만 형식이 올바르지 않음 → validateHTTPURL / url.Parse 분기를 유발.
		{"딩톡 주소가 올바르지 않음", dingTalkChannel{}, map[string]any{"webhook": "file:///" + leakProbeToken}},
		{"기업 위챗 주소가 올바르지 않음", weComChannel{}, map[string]any{"webhook": "gopher://" + leakProbeToken}},
		{"페이시 주소가 올바르지 않음", feishuChannel{}, map[string]any{"webhook": "ftp://" + leakProbeToken + "/hook"}},
		{"Telegram API 주소가 올바르지 않음", telegramChannel{}, map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": "file://" + leakProbeToken}},
		{"범용 Webhook 주소가 올바르지 않음", webhookChannel{}, map[string]any{"url": "javascript:" + leakProbeToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{Items: []Item{{Severity: "high"}}})
			if err == nil {
				t.Fatal("잘못된 설정은 에러가 나야 한다")
			}
			assertNoSecret(t, err.Error(), leakProbeToken)
		})
	}
}

func assertNoSecret(t *testing.T, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("에러 문구가 자격증명 %q 을(를) 유출했다:\n    %s",secret, text)
	}
}

func TestRedactRequestTargetKeepsOnlySchemeAndHost(t *testing.T) {
	cases := map[string]string{
		"https://oapi.dingtalk.com/robot/send?access_token=S1":    "https://oapi.dingtalk.com/…",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=S2": "https://qyapi.weixin.qq.com/…",
		"https://open.feishu.cn/open-apis/bot/v2/hook/S3":         "https://open.feishu.cn/…",
		"https://api.telegram.org/botS4/sendMessage":              "https://api.telegram.org/…",
		"http://10.0.0.5:8080/hook":                               "http://10.0.0.5:8080/…",
	}
	for in, want := range cases {
		got := redactRequestTarget(in)
		if got != want {
			t.Errorf("redactRequestTarget(%q) = %q, 기대값 %q", in, got, want)
		}
		// 비식별화 결과 자체에 원래 주소의 경로/쿼리 조각이 남아 있으면 안 된다.
		if parts := strings.SplitN(in, "://", 2); len(parts) == 2 {
			if hostAndRest := strings.SplitN(parts[1], "/", 2); len(hostAndRest) == 2 && hostAndRest[1] != "" {
				if strings.Contains(got, hostAndRest[1]) {
					t.Errorf("비식별화 후에도 경로/쿼리 조각 %q 이(가) 남아 있음: %q", hostAndRest[1], got)
				}
			}
		}
	}
	// 파싱할 수 없는 입력은 절대 원본 문자열을 그대로 돌려주면 안 된다.
	for _, bad := range []string{"", "://", "not a url", "http://"} {
		if got := redactRequestTarget(bad); strings.Contains(got, bad) && bad != "" {
			t.Errorf("파싱할 수 없는 입력 %q 이(가) %q 로 그대로 노출됨", bad, got)
		}
	}
}

// TestRedactTransportErrorStripsURL 은 *url.Error라는 구체 타입을 정면으로
// 겨냥한다: 이것은 http.Client.Do의 반환 타입이자 유출이 처음 발생하는 현장이다.
func TestRedactTransportErrorStripsURL(t *testing.T) {
	inner := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	uerr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + leakProbeToken + "/sendMessage",
		Err: inner,
	}
	got := redactTransportError(uerr)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "api.telegram.org") {
		t.Errorf("문제 추적을 위해 host는 남아 있어야 하는데 %q 를 얻음", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("문제 추적을 위해 근본 원인은 남아 있어야 하는데 %q 를 얻음", got)
	}
	// Op도 남겨야 한다(POST인지 GET인지는 문제 추적에 의미가 있다).
	if !strings.Contains(got, "Post") {
		t.Errorf("작업 이름이 남아 있어야 하는데 %q 를 얻음", got)
	}
}

// TestRedactURLsInTextHandlesFallback 은 마지막 방어 경로를 다룬다: *url.Error가
// 아닌 커스텀 에러(예: 리다이렉트 정책이 반환한 에러) 속의 주소도 마찬가지로
// 제거되어야 한다.
func TestRedactURLsInTextHandlesFallback(t *testing.T) {
	in := fmt.Sprintf("호스트 간 리다이렉트는 거부됩니다 (a.example → http://b.example/bot%s/send)", leakProbeToken)
	got := redactURLsInText(in)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "http://b.example/…") {
		t.Errorf("주소가 비식별화된 형태로 바뀌어야 하는데 %q 를 얻음", got)
	}
	// 주소가 없는 텍스트는 그대로 유지된다.
	if plain := "dial tcp: connection refused"; redactURLsInText(plain) != plain {
		t.Error("주소가 없는 텍스트는 변경되면 안 된다")
	}
}

// TestCrossHostRedirectRefused 는 "자격증명이 URL에 있다 + 호스트 간 이동을 따라간다
// = 자격증명을 넘겨준다"를 다룬다. httptest의 두 서버는 127.0.0.1의 서로 다른
// 포트에서 수신하며, 포트가 다르면 Host도 다르므로 호스트 간 이동이 된다.
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/robot/send?access_token="+leakProbeToken, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": redirector.URL + "/robot/send?access_token=" + leakProbeToken},
		Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("호스트 간 리다이렉트는 거부되어야 한다")
	}
	if hit {
		t.Fatal("이동 대상에 접근이 일어났다——자격증명이 리다이렉트를 따라 유출되었다")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestSameHostRedirectAllowed 는 반대 케이스다: 같은 호스트 내 이동(끝에 슬래시가
// 붙는 경우 등)은 여전히 쓸 수 있어야 하며, 그러지 않으면 정상적인 워크플로까지
// 함께 막힌다.
func TestSameHostRedirectAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robot/send" {
			// 같은 호스트, 같은 포트로의 이동.
			http.Redirect(w, r, "/robot/send/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"},
		Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("같은 호스트 리다이렉트는 거부되면 안 된다: %v", err)
	}
}
