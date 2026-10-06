package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets 는 메시지를 루프백 / 링크 로컬 주소로 전달하는 것을
// 허용할지 결정한다.
//
// 기본값은 거부다. 이런 주소대는 IM 봇이나 공개 메일 서버가 있을 곳이 아닌
// 반면, 거기서 닿을 수 있는 것은 민감하다: 같은 머신의 다른 서비스 관리
// 포트, 그리고 클라우드 환경의 메타데이터 엔드포인트(169.254.169.254, 인스턴스
// 자격증명을 읽어낼 수 있다). 전달 주소는 관리자가 설정하지만, XSS/CSRF로
// 탈취된 관리자 세션이나 같은 JWT를 공유하는 다른 사람이라면 설정을 바꿔서
// 응답 내용을 읽어낼 수 있다——doJSON은 4xx/5xx 응답 본문의 앞 200바이트를
// last_error에 기록하고, 전달 기록 API가 이를 그대로 보여주므로, 이것이 반맹검
// 읽기(semi-blind read) 수단이 된다.
//
// 하지만 "로컬 SMTP 릴레이"(127.0.0.1:25의 postfix)는 자체 구축 메일에서
// 흔한 구성이라, 무조건 막으면 사람들이 막힌다. 그래서 하드코딩으로 허용하는
// 대신 명시적인 탈출구를 둔다: ARTEX_NOTIFY_ALLOW_LOCAL=1을 설정하면 허용된다.
//
// AllowLocalTargetsEnv로 export한 것은 테스트가 명시적으로 이를 켤 수 있게
// 하기 위해서다——본 패키지와 server 패키지의 테스트 케이스는 127.0.0.1 상의
// httptest 가짜 수신단을 대량으로 쓰는데, 켜두지 않으면 전부 이 가드에
// 막힌다.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP 는 목표 IP가 "기본적으로 전달을 허용하지 않는" 주소대에
// 속하는지 보고한다.
//
// 루프백, 링크 로컬(클라우드 메타데이터 169.254.169.254 포함), 미지정, 멀티캐스트만
// 거부한다. RFC1918 사설망은 **거부하지 않는다**: 내부망에 자체 구축한
// Mattermost / SMTP 릴레이는 아주 흔한 정상 사용 사례이며, 이것들까지 막으면
// 실제 환경에서 기능을 쓸 수 없게 된다. 이 선택은 의도적이다——방어는 실제로
// 민감한 목표를 막아야 하며, 동시에 정상적인 배포까지 무력화시켜서는 안 된다.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6(::ffff:127.0.0.1)는 IPv4로 환원해서 판단해야 한다.
	// 그러지 않으면 검사를 우회할 수 있다.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial 은 http.Transport 다이얼러의 Control 훅으로, **연결이
// 수립되는 시점에** 목표 주소를 검사한다.
//
// 왜 다이얼 단계에서 하는가(설정 저장 시점 검증만으로 끝내지 않는 이유):
// 여기가 최종적으로 효과를 갖는 지점이다. 이는 설정 검증을 우회하는 두 가지
// 경우를 동시에 막는다——DNS 리바인딩(검증 시점에는 공개 IP로 풀렸다가 실제
// 연결 시점에 내부망으로 풀리는 경우)과 리다이렉트(이미 호스트 간 이동은
// 거부하지만, 같은 호스트 내 이동이 경로를 다른 곳으로 가리킬 수 있는 경우).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("목표 주소 %q 를 해석할 수 없습니다", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("로컬/링크 로컬 주소 %s 로의 전달은 거부됩니다 (로컬 서비스로 전달이 필요하면 %s=1 설정)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport 는 기본 Transport에 다이얼 가드 하나만 더한 것이다.
// Clone을 써서 기본값의 모든 튜닝(커넥션 풀, HTTP/2, 타임아웃, 프록시 등)을
// 그대로 유지하며, 검사 하나를 추가하려고 다른 동작을 바꾸는 일을 피한다.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient 는 모든 채널 전달이 공유하는 클라이언트다.
//
// 프로젝트의 전역 아웃바운드 프록시(server 쪽의 GlobalProxy)는 의도적으로
// **재사용하지 않는다**: 그 프록시는 침투 테스트 대상으로 가는 트래픽용이며
// 흔히 불안정한 터널인데, 알림의 가용성이 대상 네트워크의 흔들림에 인질로
// 잡혀서는 안 된다. IM 전송은 직접 연결하면 된다. 타임아웃은 15초로 둔다——
// 이보다 느린 상대는 사실상 이미 장애 상태다.
//
// 호스트 간 리다이렉트는 거부한다: 이 기능의 전달 주소는 모두 "고정된
// endpoint 하나" 형태이며 정상적으로는 다른 호스트로 리다이렉트되지 않는다;
// 그런데 이 플랫폼들의 자격증명(딩톡의 접근 토큰, 기업 위챗의 key, Telegram의
// bot token)은 **URL 안에 들어 있어서**, 호스트 간 이동을 따라가면 자격증명을
// 리다이렉트 목적지에 넘겨주는 셈이 된다. 같은 호스트 내 이동(끝에 슬래시가
// 붙는 경우 등)은 여전히 허용한다.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("리다이렉트 횟수가 너무 많습니다")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("호스트 간 리다이렉트는 거부됩니다 (%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit 은 응답 본문을 읽는 크기를 제한한다. 상대측이 비정상일 때
// 지나치게 큰 내용을 돌려줄 수 있는데, 우리에게 필요한 것은 오류 코드와
// 전달 기록에 보여줄 짧은 오류 설명뿐이다.
const respBodyLimit = 8 << 10

// doJSON 은 요청을 한 번 보내고 응답 본문을 반환한다(길이 제한 적용).
//
// payload가 nil이면 빈 body를 보낸다(GET이거나 플랫폼이 body를 요구하지 않는
// 경우). headers의 키/값은 그대로 덧붙이며, 범용 Webhook의 커스텀 헤더에 쓰인다.
//
// 이 함수의 핵심 역할은 에러를 분류하는 것이다: 네트워크 계층 실패와
// 5xx/408/429는 "재시도 가능"으로, 나머지 4xx는 "영구 실패"로 분류한다——403을
// 재시도하는 것은 같은 에러를 로그에 세 번 더 찍는 것일 뿐이다.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// 직렬화 실패는 로컬 버그다(설정 필드 타입이 맞지 않음), 재시도해도
			// 나아지지 않는다.
			return nil, Permanent(fmt.Errorf("요청 본문 구성 실패: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// URL이 잘못됨——대부분 사용자가 주소를 잘못 입력한 경우이며, 영구
		// 실패에 해당한다. 여기서도 err를 그대로 전달하면 안 된다: url.Parse의
		// 에러 문구에는 전체 주소가 들어 있다.
		return nil, Permanent(fmt.Errorf("요청 주소가 올바르지 않습니다: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// 연결 거부, DNS 실패, 타임아웃——대부분 일시적 장애이며 백오프 재시도에
		// 맡긴다.
		//
		// 에러 문구는 반드시 비식별화한 뒤 밖으로 전달해야 한다. 이유:
		// http.Client.Do가 반환하는 *url.Error는 Error()가 `Op "전체URL": 하위
		// 에러` 형태인데, 이 기능이 다루는 플랫폼들의 자격증명은 **URL 안에
		// 들어 있다**(딩톡 접근 토큰, 기업 위챗 key, 페이시 hook id, Telegram의
		// /bot<token>/). 비식별화하지 않으면 자격증명이 이 에러를 따라 네
		// 곳으로 흘러간다: notification_deliveries의 last_error(평문으로
		// 저장), 전달 기록 API의 응답(**채널 설정의 마스킹을 우회**), 서버
		// 로그, 그리고 테스트 전송 API가 프런트에 돌려주는 502 본문.
		return nil, fmt.Errorf("요청 실패: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("응답 읽기 실패: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429(제한 초과)와 408(타임아웃)은 재시도할 가치가 있다; 나머지 4xx는
	// 설정이나 권한 문제이며 재시도해도 의미가 없다.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("상대측 제한 초과 또는 타임아웃 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("상대측 서비스 이상 (HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("상대측이 요청을 거부함 (HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet 은 응답 본문을 한 줄짜리 짧은 텍스트로 압축해, 에러 메시지에 쓴다.
// 응답에는 줄바꿈과 많은 공백이 섞여 있을 수 있어서, 그대로 last_error에
// 넣으면 전달 기록 페이지의 레이아웃이 깨진다.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget 은 전달 주소를 「scheme://host/…」로 압축해, 에러
// 메시지에 쓴다.
//
// 이는 본 패키지의 유일한 주소 비식별화 기준이며, 의도적으로 **충분히
// 거칠게** 만들었다: scheme과 host를 제외한 나머지는 전부 버린다. 이유는
// URL의 어느 부분이 자격증명인지 "범용적이고 안전하게" 판단할 방법이 없기
// 때문이다:
//
//	딩톡     자격증명이 query에       /robot/send?access_token=xxx
//	기업 위챗 자격증명이 query에       /cgi-bin/webhook/send?key=xxx
//	페이시   자격증명이 **경로 끝부분**에 /open-apis/bot/v2/hook/<hook_id>
//	Telegram 자격증명이 **경로 중간**에  /bot<token>/sendMessage
//
// "유용한 부분만 남기기"를 하려면 채널별로 패치해야 하고, 하나라도 놓치면
// 그게 바로 자격증명 유출이다. host만 남겨도 문제 추적에는 충분하다(DNS
// 해석 불가, 연결 안 됨, 인증서 오류 모두 이걸로 특정할 수 있다); 구체적으로
// 어느 봇인지는 채널 설정의 마스킹된 끝자리로 알아보면 된다.
//
// 파싱에 실패하면 고정된 플레이스홀더를 반환한다——원본 문자열은 절대
// 그대로 돌려주지 않는다.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(주소를 해석할 수 없음)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError 는 전송 계층 에러에서 주소를 벗겨내고 근본 원인만
// 남긴다.
//
// *url.Error의 구조는 {Op, URL, Err}이며, Error()는 URL을 그대로 출력한다.
// 여기서는 Err 필드를 명시적으로 꺼내 Error()를 거치지 않는다——사후에 문자열
// 치환을 하는 것보다 신뢰할 수 있다. URL 인코딩/이스케이프의 다양한 변형을
// 치환으로 정확히 다루려면 빠뜨리기 쉽기 때문이다.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: 알 수 없는 에러", uerr.Op, host)
	}
	// *url.Error가 아닌 경우(예: 리다이렉트 정책이 반환하는 에러)도 주소를
	// 담고 있을 수 있으므로, 공통적으로 비식별화를 거친다.
	return redactURLsInText(err.Error())
}

// redactURLsInText 는 텍스트에 나타나는 http(s) 주소를 비식별화된 형태로
// 바꾼다.
//
// 구조화된 필드를 얻을 수 없는 에러(리다이렉트 정책 에러, 서드파티 라이브러리의
// 커스텀 에러)를 받아내는 용도다. http/https 접두사만 인식하고 공백과
// 인용부호로 끝을 구분한다——주소에는 이 두 종류의 문자가 들어가지 않는다.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
