package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes 는 s를 max 바이트를 넘지 않도록 자르며, 결과가 유효한 UTF-8이고
// 문자를 중간에서 자르지 않도록 보장한다.
//
// 왜 문자 경계로 잘라야 하는가: 기업 위챗 그룹 봇의 markdown은 4096 **바이트**
// 하드 상한을 가지며(문자 수가 아니다), 한중일 문자는 한 글자에 3바이트다.
// 바이트 단위로 그대로 자르면 한 글자가 반 토막 나서 잘못된 UTF-8이 만들어진다——
// 플랫폼 쪽에서는 메시지 전체를 거부하거나 깨진 문자로 표시한다. 여기서는 예산
// 위치에서부터 가장 가까운 rune 시작 바이트까지 거슬러 올라간다(utf8.RuneStart로
// 연속 바이트 0b10xxxxxx를 판별).
//
// max<=0 은 무제한을 의미한다. 자른 뒤에는 생략 부호를 붙이되, max가 생략 부호도
// 들어가지 못할 만큼 작으면 붙이지 않는다.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max가 생략 부호보다도 짧음: 생략 부호를 포기하고 순수하게 자르기만 해서
		// 결과가 오히려 max를 넘는 일을 피한다.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine 은 여러 줄 텍스트를 한 줄로 압축한다: 모든 공백을 접은 뒤 문자 수로
// 자른다. IM 메시지의 제목 줄에 쓰인다——요약문에는 줄바꿈이 흔한데, 그대로
// 표/제목에 넣으면 레이아웃이 깨진다. max<=0 은 길이를 제한하지 않음을 의미한다.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes 는 s를 max 문자(바이트가 아니라)를 넘지 않도록 자르며, 넘을 때
// 생략 부호를 붙인다. max<=0 은 무제한을 의미한다.
//
// TruncateBytes와의 차이는 플랫폼 기준에 있다: 기업 위챗은 바이트로, Telegram은
// 문자 수로 길이를 제한한다. 기준을 잘못 쓰면 에러는 나지 않고, 메시지가 예상보다
// 훨씬 짧게 잘릴 뿐이다(한중일 문자 1자 = 3바이트이므로, 바이트 기준으로 4096을
// 자르면 약 1365자밖에 남지 않는다), 그래서 두 함수를 모두 두고 채널별로 골라
// 써야 한다.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML 은 HTML 조각을 문자 수로 자르면서 태그가 반 토막 나지 않도록
// 보장한다.
//
// HTML을 그대로 문자 단위로 자르면 `<a href="htt` 같은 불완전한 태그가 생기고,
// 플랫폼 파서는 메시지 전체를 거부하거나 이어지는 본문을 속성값으로 삼켜버린다.
// 여기서는 먼저 문자 단위로 자른 뒤, 끝부분에 닫히지 않은 `<`가 있는지 검사해서
// 있으면 그 앞으로 되돌린다.
//
// 태그를 맞춰 닫아주지는 않는다(`</b>` 보완 등): Telegram의 HTML 파서는 닫히지
// 않은 태그를 자동으로 닫아주므로, 직접 맞춰 닫는 로직을 구현하면 속성 안의
// 인용부호, 주석, 자체 닫힘 태그까지 처리해야 해서 복잡도가 이득보다 크다.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// 끝부분이 `<`로 시작하는 조각이면(마지막 `<` 뒤에 `>`가 없으면) `<` 앞으로
	// 되돌린다.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// 끝부분이 잘린 HTML 엔터티면(예: `&amp;`가 `&amp`로 잘린 경우) 마찬가지로
	// 되돌려야 한다. 엔터티 조각은 엔터티만 인식하는 파서에서 **메시지 전체**를
	// 거부당하게 만들 수 있다——길이 상한을 넘는 요약 메시지는 원래도 흔하므로,
	// 이것 때문에 알림 전체를 잃을 이유는 없다.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount 는 예산 안에서 **완전한 형태로** 몇 건을 담을 수 있는지 계산하며,
// 요약 메시지를 건 단위로 묶는 데 쓰인다.
//
// 왜 건 단위로 묶지, 전체를 렌더링한 뒤 잘라내지 않는가: 잘라내기를 하면 뒷부분
// 항목들이 흔적도 없이 사라지는데, 그 전달 기록은 여전히 전달 완료로 표시된다——
// 메시지에도, 전달 기록에도 드러나지 않은 채 취약점이 그렇게 사라진다. 건 단위로
// 묶으면 담기지 못한 항목은 DB에 남아 다음 배치가 되고, 호출자가 받는 kept가
// 이번 메시지로 실제 전달된 건수가 된다.
//
// 파라미터: maxSize<=0 은 무제한을 의미한다; reserve 는 메시지 머리/꼬리에 남겨둘
// 여유분이다; size 는 계량 방식을 맡는다(플랫폼마다 기준이 다르다: 기업 위챗/딩톡은
// 바이트, Telegram은 문자 수——기준을 잘못 쓰면 에러는 나지 않고 다국어 메시지가
// 상한보다 훨씬 작게 눌릴 뿐이다); render 는 idx번째 항목을 실제 텍스트로
// 렌더링한다——길이는 내용에 따라 달라서 추정으로 대체할 수 없다.
//
// 항목이 하나라도 있으면 최소 1을 반환한다. 단건이 극단적으로 길 때도 그 한
// 건은 내보내고 호출자의 최종 절단에 맡긴다. 그러지 않으면 지나치게 긴 취약점
// 하나가 전체 배치를 영구히 막아버린다.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize 는 packItemCount가 쓰는 두 가지 계량 방식이며, 호출부에
// 맨 func(s string) int 클로저가 그대로 나타나지 않도록 이름을 붙였다. 그러지
// 않으면 어느 방식을 쓰는지 한눈에 알아보기 어렵다.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine 은 자산 목록을 한 줄짜리 표시 텍스트로 렌더링하며, limit개를 넘으면
// 나머지는 생략하고 총 개수를 표시한다. 취약점 하나가 수십 개 자산에 걸릴 수
// 있어서, 전부 나열하면 메시지가 넘쳐난다.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, "、")
	}
	return strings.Join(assets[:limit], "、") + " 등 " + itoa(len(assets)) + "개"
}

// itoa 는 strconv.Itoa의 짧은 별칭으로, 표시 텍스트를 이어붙이는 데만 쓰여
// 여기저기서 strconv를 import하지 않아도 되게 한다.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
