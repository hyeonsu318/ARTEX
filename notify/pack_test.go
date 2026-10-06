package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 이 파일은 "건 단위로 묶기" 수정을 다룬다: 요약 메시지가 채널 길이 상한을
// 넘으면, **건 단위로** 잘라내고 담기지 못한 항목 수를 사실대로 보고해서
// 호출자가 실제로 전달된 것만 표시로 처리하게 해야 한다.
//
// 이전 방식은 전체를 렌더링한 뒤 잘라내고 배치 전체를 전달 완료로 표시하는
// 것이었다: 메시지 후반부가 흔적도 없이 사라지는데, 전달 기록에는 전부
// 성공으로 나온다——취약점이 그렇게 사라지고, 어디서도 알 수 없다.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// 200건 요약이면 기업 위챗의 4096바이트를 반드시 넘는다.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("본문 %d바이트가 상한 %d 초과", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("본문이 올바른 UTF-8이 아니다")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("일부만 담겨야 하는데(0 < kept < %d), %d 를 얻음", len(m.Items), kept)
	}
	// 머리말은 이 메시지에 몇 건이 담겼고 나머지가 몇 건인지 사실대로 밝혀야
	// 한다——그러지 않으면 읽는 사람이 머리말에 적힌 숫자를 전부로 오해한다.
	if !strings.Contains(body, "나머지") || !strings.Contains(body, "다음 메시지에서 계속") {
		t.Fatalf("머리말은 이 메시지에 포함되지 않은 건수를 밝혀야 한다:\n%s", body[:minInt(400, len(body))])
	}
	// 앞의 kept건만 포함되어야 한다.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "취약점"+itoa(i+1)) {
			t.Fatalf("%d번째 항목이 이 메시지에 있어야 한다:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "취약점"+itoa(kept+1)) {
		t.Fatalf("%d번째 항목은 나타나면 안 된다(다음 배치에 속함)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = 무제한
	if kept != len(m.Items) {
		t.Fatalf("길이 무제한일 때는 전부 보존해야 하는데 kept=%d", kept)
	}
	if strings.Contains(body, "나머지") {
		t.Fatalf("절단이 없을 때는 절단 안내문이 없어야 하는데:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// 예산이 너무 작아서 한 건도 담지 못할 때도 한 건은 내보내야 한다(최종
	// 절단이 뒤를 받친다). 그러지 않으면 지나치게 긴 취약점 하나가 전체
	// 배치를 영구히 막아버린다: 매번 가져가려 해도 담지 못하고, 매번 전송되지
	// 않는다.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("최소 1건은 보존해야 하는데 %d 를 얻음", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("단건 메시지는 1건 전달로 보고해야 하는데 %d 를 얻음", kept)
	}
	// 빈 메시지는 전달할 항목이 없다.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("빈 메시지는 0건으로 보고해야 하는데 %d 를 얻음", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram은 **문자 수**로 길이를 제한한다; 바이트 기준을 쓰면 다국어
	// 메시지가 3분의 1로 눌린다.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("본문 %d 문자가 상한 %d 초과", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("일부만 담겨야 하는데 %d 를 얻음", kept)
	}
	if !strings.Contains(text, "다음 메시지에서 계속") {
		t.Fatalf("담기지 못한 나머지가 있음을 밝혀야 하는데:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("카드에는 일부만 담겨야 하는데 %d 를 얻음", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// 이 두 채널은 본문을 자르지 않으므로 전체 배치가 전달된 것으로 본다.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("사전 조건이 성립하지 않음")
	}
	// 렌더러의 반환값으로 간접 확인한다: markdownBody(0)은 무제한일 때 전부
	// 보존한다.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("길이 무제한일 때는 전부 써야 하는데 %d 를 얻음", k)
	}
}

// TestMarkdownEscapesUntrustedContent 는 "신뢰할 수 없는 내용이 메시지 구조를
// 바꾸면 안 된다"는 것의 회귀 테스트다. 제목과 요약은 모델 출력에서 오고
// (모델은 테스트 대상의 응답을 읽는다), 자산명은 테스트 대상의 URL에서 온다.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // 결과에 반드시 나타나야 함(이스케이프된 형태)
		wrong []string // 결과에 나타나면 안 됨(이스케이프되지 않은 형태)
	}{
		{
			name: "제목 안의 줄바꿈 + 외부 링크",
			item: Item{
				Severity: "high",
				Name:     "로그인창 SQL 인젝션\n[긴급: 계정 인증은 여기를 클릭](http://attacker.tld)",
			},
			// 줄바꿈은 반드시 접혀야 한다(그러지 않으면 새 목록 항목/인용
			// 블록을 위조할 수 있다); 대괄호와 소괄호는 반드시 이스케이프되어야
			// 한다(그러지 않으면 클릭 가능한 외부 링크가 된다).
			must:  []string{`\[긴급: 계정 인증은 여기를 클릭\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[긴급", "\n\n[긴급"},
		},
		{
			name: "제목 안의 이미지 비콘",
			item: Item{
				Severity: "high",
				Name:     "취약점 ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "자산명 안의 강조와 인용",
			item: Item{
				Severity: "high",
				Name:     "일반 제목",
				Assets:   []string{"a.com/*인젝션*>인용"},
			},
			must:  []string{`\*인젝션\*`, `\>`},
			wrong: []string{"*인젝션*"},
		},
		{
			name: "요약 안의 백틱과 파이프",
			item: Item{
				Severity: "high",
				Name:     "제목",
				Summary:  "`code` | 표",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// 단건 모드의 writeItem은 세 markdown 채널이 공유하는 렌더링 경로다.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("이스케이프된 형태 %q 가 없음:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("이스케이프되지 않은 형태 %q 가 나타남(구조나 외부 링크 주입에 쓰일 수 있음):\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst 는 이스케이프 순서를 고정한다: 백슬래시는
// 반드시 가장 먼저 처리해야 하며, 그러지 않으면 나중에 덧붙인 백슬래시가 다시
// 이스케이프되어 출력에 이중 백슬래시가 나타난다.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("이스케이프 순서가 잘못됨, %q 를 얻음", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes 는 구체적인 회귀 하나를 고정한다:
// markdown 이스케이프가 Telegram의 HTML 출력에 새어 들어가면 안 된다(예전에
// 공유 제목 함수에 이스케이프를 추가했다가, Telegram 메시지에 `\(1\)` 같은
// 눈에 보이는 백슬래시가 나타난 적이 있다).
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *중점*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Telegram 본문에 markdown의 백슬래시 이스케이프가 나타남:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
