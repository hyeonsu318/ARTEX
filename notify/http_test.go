package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 이 파일은 doJSON의 HTTP 계층 에러 분류를 다룬다.
//
// 왜 따로 테스트하는가: 각 채널 어댑터는 플랫폼 자체의 업무 에러 코드(딩톡
// errcode, 페이시 code, Telegram ok 필드)만 처리하고, **HTTP 계층**의 분류는
// doJSON이 일괄적으로 한다. 이 둘은 서로 독립된 두 겹의 방어선이다. 이것이
// 없으면 503을 반환하는 중계 게이트웨이가 영구 실패로 취급되어 재시도를 곧바로
// 포기하게 되고, 반대로 403은 재시도 가능으로 취급되어 백오프를 세 번 헛돌게
// 된다.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 성공은 에러가 아님", 200, false},
		{"429 제한 초과는 재시도 가능", 429, false},
		{"408 요청 타임아웃은 재시도 가능", 408, false},
		{"500 서버 에러는 재시도 가능", 500, false},
		{"502 게이트웨이 에러는 재시도 가능", 502, false},
		{"503 서비스 불가는 재시도 가능", 503, false},
		{"400 파라미터 오류는 영구 실패", 400, true},
		{"401 인증 실패는 영구 실패", 401, true},
		{"403 접근 금지는 영구 실패", 403, true},
		{"404 주소 없음은 영구 실패", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx는 에러가 나면 안 된다: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("2xx가 아니면 에러가 나야 한다")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("HTTP %d 의 permanent 판정이 잘못됨: 기대 %v, 얻음 %v (%v)",
					tc.status, tc.permanent, got, err)
			}
			// 상태 코드는 반드시 에러에 나타나야 한다. 그러지 않으면 사용자가 자신의
			// 설정 오류인지 상대측 장애인지 판단할 수 없다. Go의 영문 StatusText가
			// 아니라 숫자를 단언한다: 본 패키지의 문구는 한국어이므로(프로젝트의
			// 다른 부분과 일치), 숫자야말로 언어와 무관하게 안정적으로 단언할 수 있는
			// 부분이다.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("에러 메시지에 HTTP 상태 코드 %d 이(가) 있어야 하는데 %v 를 얻음", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet 은 snippet을 다룬다: 상대측이 반환한 에러
// 설명을 함께 가져와야 하며, 그러지 않으면 사용자는 "실패했다"는 것만 알고
// 상대가 왜 거부했는지 알 수 없다.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("에러가 나야 한다")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("에러 메시지에 상대측의 설명이 포함되어야 하는데 %v 를 얻음", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded 는 snippet의 형태를 제약한다:
// 상대측 응답이 그대로 last_error 컬럼과 프런트 표에 들어가므로, 여러 줄이거나
// 지나치게 길면 레이아웃과 페이로드가 깨진다.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// 줄바꿈, 탭, 5000자짜리 긴 내용이 포함된 응답.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("에러가 나야 한다")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("에러 메시지는 한 줄로 압축되어야 하는데 %q 를 얻음", msg)
	}
	// snippet 상한 200자 + 고정 접두사이므로, 전체 길이는 원본 응답보다 훨씬 작아야 한다.
	if len(msg) > 400 {
		t.Errorf("에러 메시지가 너무 김(%d 바이트), snippet으로 잘려야 한다: %q", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse 는 읽기에 상한이 있음을 확인한다: 상대측이
// 비정상적으로 매우 큰 내용을 반환해도 응답 전체를 메모리로 읽어서는 안 된다
// (전달 기록의 모든 건이 last_error를 한 부씩 저장한다).
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("에러가 나야 한다")
	}
	if len(err.Error()) > 400 {
		t.Errorf("매우 큰 응답은 길이 제한을 두고 읽어 잘라야 하는데, 에러 메시지 길이가 %d", len(err.Error()))
	}
}
