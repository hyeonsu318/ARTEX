package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// 이 파일은 관련된 두 가지 강화 조치를 다룬다:
//   ① 전달 주소가 서버를 발판 삼아 내부망 / 클라우드 메타데이터를 건드리지 못하게 한다(SSRF)
//   ② 주소 검증의 에러 메시지에 주소 속 자격증명이 노출되지 않게 한다
//
// 테스트 환경에 관해: 본 패키지의 많은 테스트 케이스는 127.0.0.1 상의 httptest
// 가짜 수신단을 쓰는데, 가드가 기본적으로 이들을 막는다. 그래서 TestMain에서
// AllowLocalTargetsEnv를 일괄적으로 켜고, 아래의 각 SSRF 케이스는 이를 명시적으로
// 지워서 **기본 거부** 동작을 단언한다.

func TestMain(m *testing.M) {
	// 일반 케이스가 로컬 가짜 수신단에 연결할 수 있게 한다; SSRF 케이스는 스스로
	// 임시로 비운다.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault 는 SSRF 방어의 핵심 단언이다:
// 기본 설정에서 루프백 주소로의 전달은 **연결 계층**에서 거부되어야 한다.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // 탈출구를 끈다 = 기본 동작
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("기본적으로 루프백 주소로의 전달을 허용하면 안 된다")
	}
	if hit {
		t.Fatal("요청이 이미 로컬 서비스에 도달했다——가드가 작동하지 않았다")
	}
	// 에러 메시지는 사용자가 허용하는 방법을 안내해야 한다(로컬 SMTP 릴레이는 정상 설정이다).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("거부 메시지는 명시적으로 허용하는 방법을 설명해야 한다: %v", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn 은 반대 케이스다: 명시적으로 켜면 반드시
// 쓸 수 있어야 하며, 그러지 않으면 로컬 postfix / 내부망 릴레이 같은 정상 배포가
// 일괄적으로 무력화된다.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("명시적으로 허용한 뒤에는 전달할 수 있어야 한다: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // 클라우드 메타데이터 엔드포인트——이 함수가 존재하는 주된 이유
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // IPv4-mapped 형식은 환원한 뒤 판단해야 하며, 그러지 않으면 우회 경로가 된다
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s 은(는) 거부되어야 한다", s)
		}
	}
	// RFC1918 사설망은 **의도적으로 허용한다**: 내부망에 자체 구축한 Mattermost /
	// SMTP 릴레이는 흔한 정상 사용 사례다. 이 단언이 그 선택을 고정한다——나중에
	// 누군가 무심코 사설망 판단을 추가하면 여기서 실패하여, 의식적인 결정을
	// 강제한다(일부 배포를 조용히 무력화하는 대신).
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s 은(는) 허용되어야 한다(사설망은 흔한 정상 전달 대상)", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets 는 설정 단계의 사전 안내를
// 다룬다: 리터럴 IP는 저장 시점에 바로 거부해야 하며, 첫 전달 실패를 기다리지
// 않는다.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s 은(는) 설정 단계에서 거부되어야 한다", raw)
		}
	}
	// 공개 주소와 사설망 주소는 평소처럼 통과한다(사설망은 다이얼 단계에 맡기며,
	// 거기서도 막지 않는다).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s 은(는) 검증을 통과해야 한다: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials 는 감사에서 지난 라운드의 누락으로
// 지적된 분기다.
//
// url.Parse가 **실패**하면 *url.Error를 반환하며, 그 Error()에는 전체 원본 주소가
// 들어 있다. 지난 라운드에서는 http.Client.Do가 반환하는 에러만 비식별화하고 여기를
// 놓쳤다; 게다가 그때 보강한 "영구 실패 경로" 케이스(file://, gopher://, ftp://)는
// 사실 모두 url.Parse가 성공적으로 파싱하고 scheme 분기를 타기 때문에, 전부
// 통과해도 이 경로가 안전하다는 증명이 되지 못했다——거짓 보증이었다.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // 잘못된 퍼센트 이스케이프
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // 포트가 숫자가 아님
		"http://[::1?access_token=" + leakProbeToken,                  // 괄호가 짝이 맞지 않음
	}
	for _, raw := range cases {
		// 먼저 이 입력이 **실제로** url.Parse를 실패시키는지 확인한다. 이 단계를
		// 빼면 케이스가 모르는 사이에 다른 분기로 흘러갈 수 있다(지난 라운드의
		// 거짓 보증이 바로 이렇게 생겼다).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q 은(는) 파싱에 실패해야 하며, 그렇지 않으면 이 케이스는 목표 분기를 커버하지 못한다", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q 은(는) 검증에 실패해야 한다", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// 채널 계층의 래핑도 주소를 노출하지 않는지 확인한다.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("잘못된 주소는 검증에 실패해야 한다")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault 는 SMTP 채널의 다이얼 가드를 다룬다.
//
// 이메일 채널은 한때 순수한 net.Dialer를 썼고, 이것이 SSRF 방어 전체에서 유일한
// 빈틈이었다: host를 169.254.169.254나 127.0.0.1로 지정하면 바로 연결되고,
// smtp.NewClient의 핸드셰이크가 실패하면 상대가 돌려준 그 줄을 에러에 담아
// last_error를 거쳐 전달 기록 API가 그대로 보여주었다——다른 채널에서는 이미
// 막은 반맹검 읽기 수단이다; "연결 거부 vs 타임아웃"의 소요 시간 차이로 포트를
// 탐지하는 것도 가능했다.
//
// 본 패키지의 TestMain은 AllowLocalTargetsEnv를 전역으로 켜 두므로(많은 케이스가
// 127.0.0.1 상의 가짜 수신단을 쓴다), 이 케이스는 스스로 이를 비워야 한다——
// 그러지 않으면 가드가 있든 없든 통과하게 되며, 이것이 바로 이 빈틈이 당시 어떤
// 테스트에도 발견되지 않은 이유다.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // 탈출구를 끈다 = 기본 동작
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("기본적으로 루프백 주소로 이메일을 전달하는 것을 허용하면 안 된다")
	}
	// 연결 자체가 수립되어서는 안 된다: 가드가 Control 훅에서 막으므로 EHLO는
	// 절대 나가지 않는다.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP 세션이 이미 수립되었다——가드가 작동하지 않았다")
	}
	// 에러 메시지는 사용자가 허용하는 방법을 안내해야 한다(로컬 postfix 릴레이는 정상 설정이다).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("거부 메시지는 명시적으로 허용하는 방법을 설명해야 한다: %v", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn 은 짝이 되는 반대 케이스다: 명시적으로
// 켜면 정상적으로 전달되어야 한다. 내부망에 자체 구축한 SMTP / 로컬 릴레이는 아주
// 흔한 배포이며, 가드가 일괄적으로 막아서는 안 된다.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("명시적으로 허용한 뒤에는 로컬 SMTP로 전달할 수 있어야 한다: %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("EHLO가 관찰되지 않았다——세션이 실제로 수립되지 않았다")
	}
}
