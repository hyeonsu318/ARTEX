package db

import (
	"regexp"
	"testing"
)

// 내장 "삭제류 API 경로" 규칙은 tool_input JSON 문자열 전체를 매칭하므로, 테스트
// 케이스는 Interceptor가 실제로 받는 subject와 동일하게 JSON 형태로 직접 준다.
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET으로 삭제 API 호출
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST로 삭제 API 호출
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // v1의 경로 규칙은 접미사를 허용하지 않았는데, 여기서 보강함
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("매칭되어야 하는데 통과됨: %s", s)
		}
	}

	// 동사 뒤에는 반드시 구분자가 와야 하며, /delivery, /details 같은 읽기 전용 경로가 잘못 차단되지 않게 한다.
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // 삭제 동사가 경로가 아니라 도메인에 나타남
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("잘못 차단됨: %s", s)
		}
	}
}
