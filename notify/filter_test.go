package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// 잘못된 JSON, 빈 입력, 타입이 맞지 않는 필드——전부 제로값 Filter, 즉
	// "필터링하지 않음"으로 퇴화해야 한다. 이 불변 조건이 "놓치기보다 더 보낸다"의
	// 귀착점이다: 여기를 에러나 반쪽 파싱으로 바꾸면, 사용자가 글자 하나만
	// 잘못 설정해도 모든 고위험 알림이 조용히 사라진다.
	cases := []struct {
		name string
		raw  string
	}{
		{"빈 입력", ""},
		{"잘못된 JSON", `{not json`},
		{"잘린 JSON", `{"min_severity":`},
		{"타입 불일치", `{"min_severity": 123, "task_ids": "abc"}`},
		{"최상위가 배열", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("잘못된 설정은 제로값 Filter로 퇴화해야 하는데 %+v 를 얻음", f)
			}
			// 제로값 Filter는 모든 이벤트에 통과해야 한다.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("제로값 Filter는 모든 이벤트에 통과해야 한다")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// 알 수 없는 등급의 순서값은 0이므로, 비어 있지 않은 기준에는 모두 걸러진다
		// (확신이 없으면 보내지 않는다).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: 기대 %v, 얻음 %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL 인젝션",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"빈 범위=제한 없음", Filter{}, true},
		{"작업 일치", Filter{TaskIDs: []int64{7}}, true},
		{"작업 불일치", Filter{TaskIDs: []int64{8}}, false},
		{"작업 다중 선택 중 일치 포함", Filter{TaskIDs: []int64{8, 7}}, true},
		{"자산 교집합 있음", Filter{AssetIDs: []int64{20, 99}}, true},
		{"자산 교집합 없음", Filter{AssetIDs: []int64{99}}, false},
		{"작업과 자산 모두 일치", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"작업은 일치하나 자산은 불일치", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("기대 %v, 얻음 %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include 가 비어 있으면 전부 수신", Filter{}, "임의 유형", true},
		{"include 일치", Filter{VulnClassInclude: []string{"SQL"}}, "SQL 인젝션", true},
		{"include 불일치", Filter{VulnClassInclude: []string{"명령 실행"}}, "SQL 인젝션", false},
		{"include 여러 단어 중 하나 일치", Filter{VulnClassInclude: []string{"명령 실행", "SQL"}}, "SQL 인젝션", true},
		{"대소문자 구분 없음", Filter{VulnClassInclude: []string{"sql"}}, "SQL 인젝션", true},
		{"exclude 일치 시 제외", Filter{VulnClassExclude: []string{"정보 유출"}}, "정보 유출", false},
		{"exclude 불일치면 통과", Filter{VulnClassExclude: []string{"정보 유출"}}, "SQL 인젝션", true},
		// 제외가 포함보다 우선: 동시에 일치하면 탈락해야 한다.
		{"제외가 포함보다 우선", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"인젝션"},
		}, "SQL 인젝션", false},
		// 공백뿐인 키워드는 무시되어야 한다. 그러지 않으면 "공백이 포함된 모든
		// 문자열에 일치"로 퇴화한다.
		{"공백 키워드는 무시됨", Filter{VulnClassInclude: []string{"", "  "}}, "SQL 인젝션", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("기대 %v, 얻음 %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// 기본값은 꺼짐: 대다수 사람이 말하는 "취약점 알림"은 새 취약점 발견을 뜻하며,
	// 상태 전환 기록이 아니다.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("상태 변경 이벤트는 켜지 않았을 때 건너뛰어야 한다")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("on_status_change를 켜면 상태 변경 이벤트가 통과해야 한다")
	}
	// 생성 이벤트는 on_status_change의 영향을 받지 않는다.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("생성 이벤트는 on_status_change에 의존하면 안 된다")
	}
}
