package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// 이것이 본 패키지에서 가장 중요한 불변 조건이다. 기업 위챗은 **바이트** 단위로
	// 길이를 제한하며, 한중일 문자는 3바이트/자이므로, 바이트 단위로 무작정 잘라내는
	// 구현은 글자를 반 토막 내서 잘못된 UTF-8을 만들어내고 플랫폼에서 거부당한다.
	// 길이가 서로소인 다양한 다국어 혼합 입력으로 모든 절단 지점을 검증한다.
	inputs := []string{
		"중문테스트내용",
		"혼합 mixed 내용 content",
		"a중b문c테d스e",
		"🔴🟠🟡🔵", // 4바이트 emoji, 잘못 자르면 더 눈에 띔
		strings.Repeat("취약점", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("입력 %q max=%d: 잘못된 UTF-8 %q 생성됨", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("입력 %q max=%d: 결과 %d바이트가 상한 초과", in, max, len(got))
			}
			// 잘리지 않았을 때는 내용이 바뀌면 안 된다.
			if len(in) <= max && got != in {
				t.Fatalf("입력 %q max=%d: 상한을 넘지 않았는데 내용이 바뀜 -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 은 무제한을 의미해야 한다")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 은 무제한을 의미해야 한다")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// max가 생략 부호 자체보다 작을 때, 생략 부호를 덧붙여서 오히려 상한을
	// 넘겨서는 안 된다.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1 일 때 결과 %q 길이 %d 가 상한 초과", got, len(got))
	}
	// 정상적인 경우에는 생략 부호가 붙어야 한다.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("생략 부호가 붙어야 하는데 %q 를 얻음", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// TruncateBytes와의 기준 차이는 반드시 유지해야 한다: Telegram은 문자 수로
	// 길이를 제한하므로, 바이트 기준을 쓰면 다국어 메시지가 1/3 토막으로 잘린다.
	s := "일이삼사오육칠팔구십"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("문자 5개를 기대했는데 %d개 (%q) 를 얻음", n, got)
	}
	// 같은 문자열을 바이트 기준으로 자르면 명백히 더 짧아야 한다.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("바이트 기준이 문자 기준과 같은 문자 수를 만들어내면 안 된다")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("첫째 줄\n\n둘째 줄\t탭 포함   여러 공백", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("모든 공백 문자를 접어야 하는데 %q 를 얻음", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("연속 공백이 남으면 안 되는데 %q 를 얻음", got)
	}
	// 잘린 뒤에도 읽을 수 있고 유효해야 한다.
	got = OneLine("일이삼사오육칠팔구십", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("문자 4개를 기대했는데 %d개 (%q) 를 얻음", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// HTML을 그대로 잘라내면 `<a href="htt` 같은 조각이 생기고, 플랫폼이 메시지
	// 전체를 거부한다.
	s := `<b>제목</b>본문본문본문<a href="https://example.com/very/long/path">자세히 보기</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: 결과 %d 문자가 상한 초과", max, n)
		}
		// 끝부분에 닫히지 않은 `<`(마지막 구간에 `<`는 있는데 `>`가 없는 경우)가
		// 남아 있으면 안 된다.
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: 끝부분 태그가 잘림 -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("자산이 없으면 빈 문자열을 반환해야 하는데 %q 를 얻음", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a、b" {
		t.Fatalf("상한을 넘지 않으면 전부 나열해야 하는데 %q 를 얻음", got)
	}
	// 상한을 넘으면 총 개수를 표시해야 한다. 그러지 않으면 읽는 사람이 나열되지
	// 않은 자산이 얼마나 더 있는지 알 수 없다.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "등 5개") {
		t.Fatalf("총 개수 5가 표시되어야 하는데 %q 를 얻음", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("빈 등급의 순서값은 0이므로 어떤 기준에도 걸러져야 한다")
	}
	if !AtLeast("critical", "") {
		t.Fatal("빈 기준은 통과시켜야 한다")
	}
	if got := StatusLabel("fixed"); got != "수정됨" {
		t.Fatalf("알 수 없는 상태로 매핑됨, %q 를 얻음", got)
	}
	// 알 수 없는 상태는 그대로 돌려줄 뿐 라벨을 임의로 만들어내지 않는다.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("알 수 없는 상태는 그대로 돌려줘야 하는데 %q 를 얻음", got)
	}
}

// TestTruncateHTMLNeverCutsEntity 는 감사에서 지적된 누락 하나를 다룬다: 절단은
// 태그를 반 토막 내는 것뿐 아니라, HTML 엔터티가 잘리는 것도 피해야 한다.
//
// `&amp;` 가 `&amp` 로 잘리면, 엔터티만 인식하는 파서는 메시지 **전체**를
// 거부할 수 있다——그리고 너무 긴 요약 메시지는 원래도 흔히 발생한다.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// 끝부분에 「&는 있는데 대응하는 ;가 없는」 엔터티 조각이 남으면 안 된다.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: 끝부분에 엔터티 조각 %q 가 남음", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: 깨진 엔터티가 발생함", max)
		}
	}
}
