package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout 는 각각 연결 수립과 전체 SMTP 세션에
// 제한을 둔다. net/smtp 자체에는 타임아웃 메커니즘이 전혀 없어서, 이 둘을
// 두지 않으면 멈춰버린 상대가 전달 goroutine을 영원히 붙잡아 둘 수 있다——
// dispatcher는 단일 goroutine으로 순차 처리하므로, 그러면 전체 알림
// 시스템이 멈추는 셈이다.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel 은 SMTP 이메일 전달을 구현한다.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// 이메일은 플랫폼 제한이 없지만, 그렇다고 도배하듯 쓰면 안 된다; 여유 있는
// 기본값을 둔다.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// 비밀번호만 마스킹한다. SMTP 호스트, 계정, 수신자는 비밀이 아니며, 이들까지 마스킹하면 편집만 번거로워진다.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port는 비밀번호를 어느 서버에 넘길지 결정하고, tls는 전송을 암호화할지
// 결정한다. 셋 중 하나라도 바뀌면 비밀번호를 다시 표명해야 한다——이렇게 하면
// "TLS 끄기"라는 동작에도 자격증명을 명시적으로 함께 제출하게 되어, 손대다
// 우연히 바뀌는 일을 막는다.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("SMTP 서버 주소가 없습니다")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("SMTP 포트가 올바르지 않습니다 (1-65535 사이여야 함)")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("발신자 주소가 없습니다")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("수신자 주소가 최소 한 개 필요합니다")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: 상대가 지원하면 업그레이드한다. 평문 세션에서는 자격증명을
	// 보낼 수 없다(아래 auth 설명 참고).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS 실패: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth는 암호화되지 않은 연결에서 자격증명을 보내는 것을
			// 거부한다(목적지가 localhost인 경우는 예외). 이것은 **올바른**
			// 보안 동작이며 우회해서는 안 되지만, 이유를 명확히 번역해 줘야
			// 한다——그러지 않으면 사용자는 "unencrypted connection"만 보고
			// 무엇을 해야 할지 알 수 없다.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("자격증명 전송 거부: 연결이 암호화되지 않았습니다. TLS를 활성화하거나, 465 포트(암시적 TLS)로 바꾸거나, \"TLS 사용\"을 체크하세요 (%w)", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP 인증 실패: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("발신자 %s 거부됨", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("수신자 %s 거부됨", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA 실패: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("이메일 본문 쓰기 실패: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("이메일 제출 실패: %w", err)
	}
	// Quit 실패는 "이메일을 서버가 이미 받았다"는 사실에 영향을 주지 않으므로,
	// 그 에러는 무시한다.
	_ = client.Quit()
	// 이메일은 길이를 자르지 않으므로(HTML 본문을 전부 보낸다), 배치 전체가
	// 전달된 것으로 본다.
	return len(m.Items), nil
}

// emailDial 는 SMTP 연결을 수립한다.
//
// implicitTLS=true면 465처럼 "연결하면 곧 TLS"인 방식을 쓰고, false면
// 25/587처럼 평문으로 연결한 뒤 STARTTLS를 하는 방식을 쓴다. 둘을 섞으면 안
// 된다: 465 포트에 평문 greeting을 보내면 바로 끊긴다.
//
// 세션 기한은 **연결 수립 지점에서** 미리 설정한다(나중에 보완하지 않는다).
// net/smtp의 Client는 내부 연결을 export되지 않은 필드에 감춰서 외부에서
// 접근할 수 없기 때문이다; 연결을 넘겨준 이상 미리 설정해 둔 deadline에만
// 의존할 수 있다. 이것은 핸드셰이크 단계의 블로킹도 함께 커버한다.
// Control에 blockInternalDial을 걸어 HTTP 계열 채널과 같은 가드를 공유한다.
// 걸지 않으면 SMTP가 전체 SSRF 방어의 빈틈이 된다: host에 169.254.169.254나
// 127.0.0.1을 넣으면 바로 연결되고, smtp.NewClient의 핸드셰이크가 실패하면
// 상대가 돌려준 그 줄을 에러에 담아 last_error를 거쳐 전달 기록 API가
// 그대로 보여주어, 반맹검 읽기 수단이 된다; "연결 거부 vs 타임아웃"의 소요
// 시간 차이로 포트를 탐지하는 것도 가능하다. 다이얼 단계가 최종적으로
// 효과를 갖는 지점이며, DNS 리바인딩도 커버한다.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("SMTP 서버 연결 실패: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP 핸드셰이크 실패: %w", err)
	}
	return client, nil
}

// smtpStageError 는 SMTP 응답 코드에 따라 어떤 단계의 실패를 "재시도 가능"과
// "영구 실패"로 나눈다.
//
// 왜 구분해야 하는가: SMTP의 4xx와 5xx는 의미가 완전히 다르다——
//   - 4xx(450 그레이리스팅, 451 로컬 오류, 452 저장 공간 부족)는 **임시**
//     거부이며, 나중에 재시도하는 것이 정석이다; 특히 그레이리스팅은 첫
//     전달 시도마다 거의 매번 마주친다.
//   - 5xx(550 사용자 없음, 553 주소 올바르지 않음)는 영구 거부이며,
//     재시도해도 의미가 없다.
//
// 모두 영구 실패로 판정하면, 그레이리스팅을 쓰는 메일 서버에서는 **모든**
// 전송이 첫 시도 후 failed로 떨어진다——그런데 이런 종류의 실패야말로
// 자동 재시도가 가장 쓸모 있는 상황이다. 응답 코드는 에러 문구의 앞 세 자리
// 숫자에서 가져온다; 코드를 얻지 못하면 재시도 가능으로 처리한다(파싱이
// 안 됐다고 해서 일시적일 수 있는 장애를 사망 판정하기보다는, 한 번 더
// 시도하는 쪽을 택한다).
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode 는 SMTP 에러 문구 앞부분의 세 자리 응답 코드를 가져오며,
// 얻지 못하면 0을 반환한다. net/smtp는 에러 코드 필드를 export하지 않으므로
// 문구에서 꺼낼 수밖에 없다; 형식은 「450 4.7.1 ...」이다.
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage 는 완전한 RFC 5322 이메일을 구성한다.
//
// 본문을 base64로 인코딩하는 데는 두 가지 이유가 있다: 첫째, SMTP는 한 줄이
// 1000바이트를 넘지 않도록 규정하는데, HTML 본문(특히 요약 이메일)은 지나치게
// 긴 줄이 생기기 쉽다; 둘째, base64는 "."으로 시작하는 줄이 생기지 않아서
// SMTP의 점 이스케이프 문제를 피할 수 있다.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// 다국어 제목은 RFC 2047로 인코딩해야 하며, 그러지 않으면 클라이언트에서
	// 깨진 문자로 표시된다.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// 이메일에는 하드한 길이 상한이 없으므로 본문을 자르지 않는다.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64는 RFC 2045에 맞춰 76자마다 줄을 바꾼다.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}
