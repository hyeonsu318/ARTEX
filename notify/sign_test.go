package notify

import (
	"net/url"
	"testing"
	"time"
)

// 서명 기준값은 OpenSSL로 독립적으로 계산한 것이며, 본 패키지 자체 구현으로
// 만든 것이 아니다——그러지 않으면 "코드가 바뀌지 않았다"만 증명할 뿐
// "알고리즘이 맞다"는 증명하지 못한다.
//
//	TS=1700000000000, SECRET=SECtest123
//	딩톡: printf '%s\n%s' "$TS" "$SECRET" | openssl dgst -sha256 -hmac "$SECRET" -binary | openssl base64 -A
//	      -> w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE=
//	페이시: printf '' | openssl dgst -sha256 -hmac "$(printf '%s\n%s' "$TS" "$SECRET")" -binary | openssl base64 -A
//	      -> Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo=
const (
	signTestTSMillis = int64(1700000000000)
	signTestSecret   = "SECtest123"
	dingTalkExpected = "w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE="
	feishuExpected   = "Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo="
)

func TestDingTalkSignMatchesReference(t *testing.T) {
	got, err := dingTalkSignedURL("https://oapi.dingtalk.com/robot/send?access_token=tok", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatalf("서명 실패: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("생성된 주소를 파싱할 수 없음: %v", err)
	}
	q := u.Query()
	if q.Get("sign") != dingTalkExpected {
		t.Errorf("서명 불일치\n기대 %s\n얻음 %s", dingTalkExpected, q.Get("sign"))
	}
	if q.Get("timestamp") != "1700000000000" {
		t.Errorf("타임스탬프는 밀리초 단위로 그대로 붙어야 하는데 %q 를 얻음", q.Get("timestamp"))
	}
	// 기존 query 파라미터(access_token)가 서명으로 덮어써져서는 안 된다.
	if q.Get("access_token") != "tok" {
		t.Errorf("기존 query 파라미터가 사라짐, %q 를 얻음", q.Get("access_token"))
	}
}

func TestFeishuSignMatchesReference(t *testing.T) {
	got := feishuSign("1700000000000", signTestSecret)
	if got != feishuExpected {
		t.Errorf("서명 불일치\n기대 %s\n얻음 %s", feishuExpected, got)
	}
}

// TestSignAlgorithmsDiffer 는 두 플랫폼의 알고리즘 차이를 고정한다. 둘은 서로의 파라미터 순서가 뒤바뀌어 있다
// (딩톡은 key=secret, 페이시는 key=서명할 문자열), 다른 쪽을 베껴 쓰면 반드시 검증에 실패한다,
// 이 케이스는 이후 리팩터링에서 둘을 같은 함수로 합치지 않도록 보장한다.
func TestSignAlgorithmsDiffer(t *testing.T) {
	ts := "1700000000000"
	dingURL, err := dingTalkSignedURL("https://example.com/hook", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	dq, _ := url.Parse(dingURL)
	if dq.Query().Get("sign") == feishuSign(ts, signTestSecret) {
		t.Fatal("딩톡과 페이시의 서명이 같다면, 둘 중 한쪽의 알고리즘 구현이 틀렸다는 뜻이다")
	}
}

func TestDingTalkNoSecretLeavesURLUntouched(t *testing.T) {
	// 서명을 켜지 않은 봇: timestamp/sign 파라미터를 임의로 추가해서는 안 된다.
	const hook = "https://oapi.dingtalk.com/robot/send?access_token=tok"
	got, err := dingTalkSignedURL(hook, "", time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	if got != hook {
		t.Fatalf("설정하지 않았을 때 secret 주소는 변경되면 안 되는데 %q 를 얻음", got)
	}
}

func TestValidateHTTPURL(t *testing.T) {
	ok := []string{"https://example.com/hook", "http://10.0.0.1:8080/x?y=1"}
	for _, s := range ok {
		if err := validateHTTPURL(s); err != nil {
			t.Errorf("%q 은(는) 허용되어야 하는데: %v", s, err)
		}
	}
	// file:// 같은 것은 허용하면 안 된다——http.Client가 이들을 처리하는 방식은
	// 예상 범위를 벗어난다.
	bad := []string{"", "file:///etc/passwd", "ftp://example.com", "https://", "gopher://x"}
	for _, s := range bad {
		if err := validateHTTPURL(s); err == nil {
			t.Errorf("%q 은(는) 거부되어야 한다", s)
		}
	}
}
