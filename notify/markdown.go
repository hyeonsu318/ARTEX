package notify

import (
	"fmt"
	"strings"
)

// 이 파일은 「Markdown 계열」 채널(딩톡, 기업 위챗)이 공유하는 메시지 렌더링이다.
// 페이시는 카드 JSON, Telegram은 HTML, 이메일은 HTML을 쓰며 각자 어댑터 안에서
// 렌더링한다.

// maxAssetsShown 은 메시지에 최대 몇 개의 자산을 나열할지다. 취약점 하나가
// 수십 개 자산에 걸릴 수 있는데, 전부 나열하면 메시지가 넘쳐나고 정보 가치도
// 없다——4번째 이후의 도메인은 IM에서 아무도 보지 않는다.
const maxAssetsShown = 3

// maxSummaryRunes 는 요약을 몇 글자로 압축할지다. IM 메시지는 "자세히 보러
// 가라는 안내"일 뿐 보고서 본체가 아니며, 전체 내용은 플랫폼에 있다.
const maxSummaryRunes = 120

// markdownReservedBytes 는 메시지 머리말(요약 줄 + 등급 분포 + 발생할 수 있는
// 절단 안내문)과 꼬리말(플랫폼 링크)에 남겨둘 여유분이다. 건 단위로 묶을 때 이
// 부분을 예산에서 미리 빼 둬서 머리말/꼬리말이 잘리지 않도록 보장한다——
// 머리말/꼬리말이 잘리면 읽는 사람은 "이게 몇 번째 배치인지, 아직 몇 건이 더
// 있는지"조차 알 수 없게 된다.
const markdownReservedBytes = 320

// markdownEscape 는 markdown 메타문자를 이스케이프한다.
//
// 왜 반드시 해야 하는가: 취약점 제목, 요약, 유형, 자산 표시명 모두 **신뢰할 수
// 없는 출처**에서 온다——제목과 요약은 모델 출력에서 나오고(모델은 테스트
// 대상의 응답을 읽는다), 자산의 url은 스캔으로 얻은 완전한 URL이다(대상이
// 조작할 수 있는 쿼리 문자열 포함). 이스케이프하지 않으면, 제목이
//
//	로그인창 SQL 인젝션\n[긴급: 계정 인증은 여기를 클릭](http://attacker.tld)
//
// 인 취약점이 보안 엔지니어의 딩톡/페이시에서 **클릭 가능한 외부 링크**로
// 렌더링되고; `![](http://attacker.tld/beacon)`는 렌더링 시점에 클라이언트가
// 가져오게 되어, "이 취약점이 이미 열람됨"을 알리고 열람자의 IP를 유출하는
// 셈이 된다. 악의가 없는 내용이라도, 주입된 볼드체나 인용 블록이 아래의
// 심각한 취약점을 접힌 선 밖으로 밀어낼 수 있다.
//
// 이스케이프 대상은 제목/링크/강조/목록/인용/취소선처럼 구조를 바꾸거나
// 클릭 가능한 요소를 만들어내는 문자들이다. `\`는 반드시 가장 먼저 처리해야
// 한다. 그러지 않으면 나중에 덧붙인 백슬래시가 다시 이스케이프된다.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText 는 신뢰할 수 없는 텍스트를 한 줄로 압축하고 이스케이프해서,
// markdown 본문에 쓸 수 있게 한다. 한 줄로 압축하는 것은 이스케이프만큼이나
// 중요한 나머지 절반이다: 줄바꿈 자체가 새로운 목록 항목이나 인용 블록을
// 위조해낼 수 있는데, 이스케이프 문자로는 막을 수 없다.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle 은 메시지 제목(IM 플랫폼의 제목 줄/카드 제목)을 반환하며,
// 내용은 **이스케이프되지 않은 원문**이다.
//
// 여기서 일부러 이스케이프하지 않는다: 이 제목은 네 가지 맥락의 렌더러가
// 공유한다——markdown 본문, Telegram의 HTML, 페이시 카드의 plain_text, 그리고
// 범용 Webhook의 JSON과 이메일 제목. 맥락마다 이스케이프 규칙이 다르므로
// (markdown 이스케이프를 HTML에 넣으면 눈에 보이는 백슬래시가 남고, JSON에
// 넣으면 데이터가 오염된다), 이스케이프는 각자의 출력 쪽에서 책임져야 한다.
// writeItem / feishuItemLines / telegramEscape 참고. 예전에 공유 함수에
// markdown 이스케이프를 넣었다가, Telegram 메시지에 `\(1\)` 같은 눈에 보이는
// 백슬래시가 나타난 적이 있다.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("취약점 요약 · 총 %d건", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "취약점 알림"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody 는 메시지 본문을 렌더링하며, 본문과 **실제로 쓰인 항목 수**를
// 반환한다.
//
// 반환값 kept는 이번 전달에서 실제로 전달된 항목 수이며, 호출자는 이를 근거로
// 앞의 kept개만 전달 완료로 표시해야 한다——채널 길이 상한에 걸려 밀려난
// 항목은 다음 배치로 남겨야지, 함께 성공으로 표시해서는 안 된다. 이것이 바로
// "조용한 유실"의 원인이다: 메시지는 잘렸는데 전달 기록은 전부 전달된 것으로
// 나와서, 뒷부분이 전혀 전송되지 않았다는 것을 어디서도 알 수 없다.
//
// maxBytes<=0 은 무제한을 의미한다.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// 단건 메시지는 너무 길어도 그대로 보낸다(최종 절단이 뒤를 받친다):
		// 취약점의 일부 정보라도 보내는 것이 아예 안 보내는 것보다 낫다.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[플랫폼에서 전체 보기](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro 는 요약 메시지의 시작 부분을 렌더링한다: 시간 범위, 건수,
// 등급 분포. 이 정보가 있으면 요약을 받은 사람이 플랫폼에 들어가지 않고도
// 이 배치를 즉시 처리해야 하는지 판단할 수 있다.
//
// items는 **실제로 담긴** 항목이고, total은 이번 배치의 전체 건수다. 둘이
// 다르면 "나머지 몇 건이 다음 메시지에 있는지"를 반드시 밝혀야 한다——그러지
// 않으면 읽는 사람은 메시지 머리에 적힌 숫자가 전부라고 오해하고, 전송되지
// 않은 나머지 항목은 화면 어디에도 나타나지 않는다.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**최근 %d분간 새로 발견된 취약점 %d건**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**새로 발견된 취약점 %d건**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(이 메시지에는 앞 %d건만 표시, 나머지 %d건은 다음 메시지에서 계속)", len(items), extra)
	}
	// 등급별 분포를 제시해, 심각한 항목이 있는지 한눈에 보이게 한다. **이
	// 메시지에 실제로 담긴** 항목만 집계해, "심각 3건"이라는 숫자가 아래에서
	// 셀 수 있는 항목과 일치하도록 보장한다.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem 은 취약점 항목 하나를 렌더링한다.
//
// prefix는 요약 목록의 번호에 쓰인다; single=true면 전체 버전(요약과 링크
// 포함)을 렌더링하고, 요약 목록에서는 한 줄짜리 요약만 렌더링한다——그러지
// 않으면 50건 요약이 긴 문서가 되어버린다.
//
// 외부에서 온 모든 내용(제목/유형/자산/요약)은 markdownText를 거친다: 한 줄로
// 압축 + 이스케이프. 링크는 관리자가 설정한 public_base_url로 만들어지므로
// 신뢰할 수 없는 내용이 아니고, 클릭 가능한 링크여야 하므로 그대로 출력한다.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// 요약 모드: 한 줄로 표시하고, 자산과 요약을 압축해 뒤에 붙인다.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**상태 변경**：%s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**유형**：%s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**자산**：%s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**요약**：%s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[자세히 보기](%s)\n", it.DetailURL)
	}
}
