package notify

import (
	"errors"
	"strings"
	"testing"
)

func TestMaskedValueHidesBodyButKeepsTailHint(t *testing.T) {
	const secret = "https://oapi.dingtalk.com/robot/send?access_token=abcdef123456"
	got := MaskedValue(secret)
	if strings.Contains(got, "abcdef123456") {
		t.Fatalf("마스킹된 값이 자격증명 전체를 유출했다: %q", got)
	}
	if strings.Contains(got, "oapi.dingtalk.com") {
		t.Fatalf("마스킹된 값은 주소 본체를 노출하면 안 된다: %q", got)
	}
	// 끝 6자리는 남겨야 사용자가 어느 봇인지 알아볼 수 있다.
	if !strings.HasSuffix(got, "123456") {
		t.Fatalf("식별 힌트로 끝 6자리를 남겨야 한다: %q", got)
	}
	if !IsMasked(got) {
		t.Fatalf("마스킹된 값은 IsMasked로 식별되어야 한다: %q", got)
	}
}

func TestMaskedValueShortSecretGivesNoHint(t *testing.T) {
	// 짧은 자격증명도 끝 6자리를 노출하면, 자격증명 전체를 노출하는 셈이다.
	for _, s := range []string{"abc", "abcdef", ""} {
		got := MaskedValue(s)
		if got != MaskedPrefix {
			t.Fatalf("길이 %d 의 자격증명은 끝부분 힌트를 주면 안 되는데 %q 를 얻음", len(s), got)
		}
		if s != "" && strings.Contains(got, s) {
			t.Fatalf("마스킹된 값에 원래 값이 포함됨: %q", got)
		}
	}
}

func TestMaskConfigMasksOnlySecrets(t *testing.T) {
	cfg := map[string]any{
		"webhook": "https://example.com/hook?token=SECRETVALUE",
		"secret":  "SECtest123456",
		"port":    float64(587),
		"host":    "smtp.example.com",
	}
	masked := MaskConfig(KindDingTalk, cfg)
	for _, k := range []string{"webhook", "secret"} {
		s, _ := masked[k].(string)
		if !IsMasked(s) {
			t.Errorf("%s 은(는) 마스킹되어야 하는데 %q 를 얻음", k, s)
		}
	}
	// 자격증명이 아닌 필드는 그대로 유지해야 하며, 그러지 않으면 UI가 표시할 수 없다.
	if masked["port"] != float64(587) {
		t.Errorf("자격증명이 아닌 필드 port 는 변경되면 안 된다: %v", masked["port"])
	}
}

func TestMaskConfigUnknownKindReturnsEmpty(t *testing.T) {
	// 채널 유형을 식별할 수 없을 때는 자격증명이 들어 있을 수 있는 원본 내용을
	// 돌려주기보다 UI에 빈 설정을 표시하는 쪽이 낫다.
	got := MaskConfig("nope", map[string]any{"webhook": "https://x/y?token=LEAK"})
	if len(got) != 0 {
		t.Fatalf("알 수 없는 채널 유형은 빈 설정을 반환해야 하는데 %v 를 얻음", got)
	}
}

func TestMaskConfigDoesNotMutateInput(t *testing.T) {
	// 마스킹은 표시 계층의 동작이므로, 거꾸로 DB의 실제 값을 바꿔서는 안 된다.
	cfg := map[string]any{"webhook": "https://example.com/hook", "secret": "SECtest123456"}
	_ = MaskConfig(KindDingTalk, cfg)
	if IsMasked(cfg["secret"].(string)) {
		t.Fatal("MaskConfig가 입력 인자를 수정했다. 실제 자격증명이 마스킹된 값으로 덮어써지게 된다")
	}
}

func TestMergeConfigKeepsStoredOnMaskedIncoming(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET", "method": "POST"}
	// 사용자는 method만 바꿨고, 브라우저가 제출한 것은 마스킹된 값 + 새 method다.
	incoming := map[string]any{
		"webhook": MaskedValue("https://real/hook"),
		"secret":  MaskedValue("REALSECRET"),
		"method":  "PUT",
	}
	got := MergeConfig(stored, incoming)
	if got["webhook"] != "https://real/hook" || got["secret"] != "REALSECRET" {
		t.Fatalf("마스킹된 필드는 DB의 원래 값을 유지해야 하는데 %v 를 얻음", got)
	}
	if got["method"] != "PUT" {
		t.Fatalf("수정된 필드는 반영되어야 하는데 %v 를 얻음", got["method"])
	}
}

func TestMergeConfigEmptyStringClears(t *testing.T) {
	stored := map[string]any{"webhook": "https://real/hook", "secret": "REALSECRET"}
	got := MergeConfig(stored, map[string]any{"secret": ""})
	if _, ok := got["secret"]; ok {
		t.Fatalf("빈 문자열은 해당 필드를 비워야 하는데 %v 를 얻음", got)
	}
	// 언급되지 않은 필드는 유지한다(부분 업데이트 의미).
	if got["webhook"] != "https://real/hook" {
		t.Fatalf("언급되지 않은 필드는 유지되어야 하는데 %v 를 얻음", got)
	}
}

func TestMergeConfigKeepsUnmentionedStoredKeys(t *testing.T) {
	stored := map[string]any{"host": "smtp.example.com", "port": float64(587), "password": "pw"}
	got := MergeConfig(stored, map[string]any{"port": float64(465)})
	if got["host"] != "smtp.example.com" || got["password"] != "pw" {
		t.Fatalf("언급되지 않은 필드는 유지되어야 하는데 %v 를 얻음", got)
	}
	if got["port"] != float64(465) {
		t.Fatalf("언급된 필드는 갱신되어야 하는데 %v 를 얻음", got["port"])
	}
}

// TestPrepareConfigUpdateBlocksDestinationSwap 은 본 패키지에서 가장 중요한 보안
// 불변 조건이다: **목적지 주소를 바꾸면서 기존 자격증명을 함께 가져갈 수 없다**.
//
// 이 케이스들은 바로 공격 형태의 입력(주소만 바꾸고 자격증명은 언급하지 않음)을
// 쓴다. "방어 로직에 맞는 올바른 입력"만 테스트하면, 방어가 작동하지 않아도
// 전부 통과해 버린다.
func TestPrepareConfigUpdateBlocksDestinationSwap(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
		// wantMissing 은 이름이 지목될 것으로 기대하는 자격증명 키다.
		wantMissing string
	}{
		{
			name: "범용 Webhook 주소를 바꾸면서 Authorization 헤더를 유지하려 함",
			kind: KindWebhook,
			stored: map[string]any{
				"url":     "https://legit.example.com/hook",
				"headers": map[string]any{"Authorization": "Bearer REAL-TOKEN"},
			},
			incoming:    map[string]any{"url": "https://attacker.tld/c"},
			wantMissing: "headers",
		},
		{
			name:        "Telegram base_url 을 바꾸면서 Bot Token 을 다른 엔드포인트로 보내려 함",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld"},
			wantMissing: "bot_token",
		},
		{
			name:        "이메일 SMTP 호스트를 바꾸면서 비밀번호를 넘기려 함",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"host": "smtp.attacker.tld"},
			wantMissing: "password",
		},
		{
			name:        "이메일 TLS 를 끌 때도 비밀번호를 다시 표명해야 함",
			kind:        KindEmail,
			stored:      map[string]any{"host": "smtp.corp.com", "port": 587, "tls": false, "password": "REALPW", "from": "a@b.c", "to": []any{"d@e.f"}},
			incoming:    map[string]any{"tls": true},
			wantMissing: "password",
		},
		{
			// 마스킹된 값 = "기존 자격증명 계속 사용"이므로, 주소가 바뀌는 상황에서는 마찬가지로 거부해야 한다.
			name:        "마스킹된 자격증명 반송 + 새 주소",
			kind:        KindTelegram,
			stored:      map[string]any{"bot_token": "123456:REAL", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming:    map[string]any{"base_url": "https://attacker.tld", "bot_token": MaskedValue("123456:REAL")},
			wantMissing: "bot_token",
		},
		{
			name:        "딩톡 Webhook 을 바꾸면서 서명 키를 유지하려 함",
			kind:        KindDingTalk,
			stored:      map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=OLD", "secret": "REALSEC"},
			incoming:    map[string]any{"webhook": "https://attacker.tld/hook"},
			wantMissing: "secret",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err == nil {
				t.Fatalf("주소를 바꾸면서 자격증명을 다시 표명하지 않았으면 거부되어야 하는데, 설정 %v 를 얻음", merged)
			}
			var target *ErrDestinationChangedWithoutCredentials
			if !errors.As(err, &target) {
				t.Fatalf("API가 실행 가능한 안내를 줄 수 있도록 전용 에러 타입을 반환해야 하는데 %T: %v 를 얻음", err, err)
			}
			found := false
			for _, m := range target.Missing {
				if m == tc.wantMissing {
					found = true
				}
			}
			if !found {
				t.Fatalf("누락된 자격증명 키 %q 이(가) 지목되어야 하는데 %v 를 얻음", tc.wantMissing, target.Missing)
			}
			// 에러 메시지는 작업자가 어떻게 고쳐야 하는지 안내할 수 있어야 한다.
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("에러 메시지에 %q 이(가) 언급되어야 한다: %v", tc.wantMissing, err)
			}
		})
	}
}

// TestPrepareConfigUpdateAllowsLegitimateEdits 는 반대 케이스다: 정상적인 편집이
// 잘못 막혀서는 안 된다. 그러지 않으면 이 방어는 "너무 귀찮다"는 이유로
// 우회되거나 삭제된다.
func TestPrepareConfigUpdateAllowsLegitimateEdits(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		stored   map[string]any
		incoming map[string]any
	}{
		{
			name:     "이름만 변경(설정은 그대로 반송)",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "headers": map[string]any{"Authorization": "Bearer REAL"}},
			incoming: map[string]any{"url": MaskedValue("https://legit.example.com/hook")},
		},
		{
			name:     "요청 메서드만 변경, 주소와 자격증명은 그대로",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://legit.example.com/hook", "method": "POST"},
			incoming: map[string]any{"method": "PUT"},
		},
		{
			name:     "주소를 바꾸면서 **동시에** 새 자격증명을 제공",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": map[string]any{"Authorization": "Bearer NEW"}},
		},
		{
			name:     "주소를 바꾸면서 자격증명이 더 필요 없음을 명시",
			kind:     KindWebhook,
			stored:   map[string]any{"url": "https://old.example.com/hook", "headers": map[string]any{"Authorization": "Bearer OLD"}},
			incoming: map[string]any{"url": "https://new.example.com/hook", "headers": ""},
		},
		{
			name:     "Telegram chat_id 변경(목적지가 아님)",
			kind:     KindTelegram,
			stored:   map[string]any{"bot_token": "t", "chat_id": "1", "base_url": "https://api.telegram.org"},
			incoming: map[string]any{"chat_id": "-100200"},
		},
		{
			name:     "이메일 수신자 변경(목적지가 아님)",
			kind:     KindEmail,
			stored:   map[string]any{"host": "smtp.corp.com", "port": 587, "password": "PW", "from": "a@b.c", "to": []any{"x@y.z"}},
			incoming: map[string]any{"to": []any{"new@y.z"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PrepareConfigUpdate(tc.kind, tc.stored, tc.incoming)
			if err != nil {
				t.Fatalf("정상적인 편집이 잘못 막힘: %v", err)
			}
			if merged == nil {
				t.Fatal("병합 결과를 반환해야 한다")
			}
		})
	}
}

// TestPrepareConfigUpdatePortTypeTolerance 는 잘못 판정하기 쉬운 세부 사항을 다룬다:
// 프런트가 제출한 포트는 JSON number(float64)이고 DB에서 읽어온 것도 float64이지만,
// 두 값의 타입이 다를 수 있다(예: int vs float64). ==로 비교하면 "안 바뀜"을
// "바뀜"으로 판정해, 이름만 바꾼 사용자에게 "비밀번호를 다시 입력하세요"가
// 뜨게 된다——오경보는 사람들이 이 방어를 더 이상 신뢰하지 않게 만든다.
func TestPrepareConfigUpdatePortTypeTolerance(t *testing.T) {
	stored := map[string]any{"host": "smtp.corp.com", "port": float64(587), "password": "PW"}
	// 같은 포트를 int 형태로 제출.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 587}); err != nil {
		t.Fatalf("포트 값이 같으면(타입만 다름) 주소 변경으로 판정되면 안 된다: %v", err)
	}
	// 포트를 실제로 바꾸면 반드시 막아야 한다.
	if _, err := PrepareConfigUpdate(KindEmail, stored, map[string]any{"port": 25}); err == nil {
		t.Fatal("포트 변경은 막혀야 한다")
	}
}

// TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination 은 "비워둘 수 있는
// 목적지 필드" 경로를 다룬다: Telegram의 base_url을 비워두면 공식 API 주소를 쓴다.
//
// 예전에는 이 때문에 채널이 두 번째 저장부터 영구히 저장에 실패했다:
//
//	새로 만들 때 DB에 base_url:""이 저장됨(생성 경로는 프런트가 제출한 config를 그대로 저장하며 MergeConfig를 거치지 않음)
//	→ 첫 저장에서 MergeConfig가 빈 문자열을 명시적 비움으로 보고 해당 키를 delete
//	→ 두 번째 저장에서 incoming은 여전히 ""인데 stored에는 이미 이 키가 없어 "주소가 바뀜"으로 판정
//	→ bot_token은 마스킹된 값 → 400 "목적지 주소가 변경되었습니다. 자격증명 필드도 함께 다시 입력하세요"
//
// 사용자는 아무것도 바꾸지 않았는데도, Bot Token을 다시 붙여넣지 않으면 이후로 저장할 수 없었다.
func TestPrepareConfigUpdateSurvivesRepeatedSaveWithBlankDestination(t *testing.T) {
	stored := map[string]any{"bot_token": "123:ABC", "chat_id": "-100", "base_url": ""}

	// 프런트의 buildConfig()는 이 채널의 모든 필드 정의에 대해 값을 하나씩 제출한다:
	// 자격증명은 마스킹 값으로 채우고, 빈 텍스트 상자는 빈 문자열로 제출한다.
	// 여기서는 "바뀐 키"만 제출하지 않고 그 출력을 그대로 재현한다.
	submit := func() map[string]any {
		return map[string]any{
			"bot_token": MaskedValue("123:ABC"),
			"chat_id":   "-100",
			"base_url":  "",
		}
	}

	// 첫 번째 저장: 채널 이름만 바꿨고, config는 그대로 반송한다.
	merged, err := PrepareConfigUpdate(KindTelegram, stored, submit())
	if err != nil {
		t.Fatalf("첫 번째 저장이 잘못 막힘: %v", err)
	}
	if _, ok := merged["base_url"]; ok {
		t.Fatal("전제가 바뀜: 빈 문자열은 MergeConfig가 삭제해야 한다——이 케이스가 다루려는 것이 바로 \"키가 사라진 뒤\"의 단계다")
	}

	// 두 번째 저장: 제출 내용이 이전과 완전히 같고, 사용자는 아무것도 바꾸지 않았다.
	merged2, err := PrepareConfigUpdate(KindTelegram, merged, submit())
	if err != nil {
		t.Fatalf("두 번째 저장이 잘못 막힘(사용자는 아무것도 바꾸지 않음): %v", err)
	}
	// 세 번째: "한 번만 틀린 것"이 아니라 안정적으로 저장되는지 확인한다.
	if _, err := PrepareConfigUpdate(KindTelegram, merged2, submit()); err != nil {
		t.Fatalf("세 번째 저장이 잘못 막힘: %v", err)
	}
	// 자격증명은 끝까지 유지되어야 하며, 빈 문자열 처리 로직에 휩쓸려 지워지면 안 된다.
	if got := merged2["bot_token"]; got != "123:ABC" {
		t.Fatalf("Bot Token 은 원래 값을 유지해야 하는데 %v 를 얻음", got)
	}
}

// TestPrepareConfigUpdateStillGuardsBlankDestinationChanges 는 앞 케이스와 짝을
// 이루는 단언이다: 빈 문자열과 "키가 없음"을 동등하게 취급한다고 해서 실제 주소
// 변경까지 통과시켜서는 **안 된다**. 두 방향 모두 자격증명이 밖으로 새어 나가는
// 실제 경로다——Telegram의 Bot Token은 URL 경로에 들어 있으므로, base_url을
// 바꾸면 Token을 새 주소로 보내는 셈이다.
func TestPrepareConfigUpdateStillGuardsBlankDestinationChanges(t *testing.T) {
	// 방향 1: "비어 있음"(공식 주소)에서 자체 구축 주소로 변경.
	official := map[string]any{"bot_token": "123:ABC", "chat_id": "-100"}
	if _, err := PrepareConfigUpdate(KindTelegram, official, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "https://tg-proxy.attacker.tld",
	}); err == nil {
		t.Fatal("공식 주소에서 자체 구축 주소로 바꿀 때는 Token을 다시 입력하도록 요구해야 한다")
	}

	// 방향 2: 자체 구축 주소를 비우는 것(= 공식 API로 되돌림)도 주소 변경이다.
	proxied := map[string]any{"bot_token": "123:ABC", "base_url": "https://proxy.internal/bot"}
	if _, err := PrepareConfigUpdate(KindTelegram, proxied, map[string]any{
		"bot_token": MaskedValue("123:ABC"),
		"base_url":  "",
	}); err == nil {
		t.Fatal("자체 구축 주소를 비우는 것(공식 API로 되돌림)도 주소 변경이므로, Token을 다시 입력하도록 요구해야 한다")
	}
}

func TestDestinationKeysDeclaredForEveryKind(t *testing.T) {
	//SecretKeys 같은 이유로: 채널이 목적지 키를 선언하는 것을 잊으면 PrepareConfigUpdate가 그 채널을 보호하지 못한다.
	for kind, ch := range registry {
		if len(ch.DestinationKeys()) == 0 {
			t.Errorf("채널 %s 이(가) 목적지 키를 선언하지 않았으며, 주소 변경으로 자격증명이 딸려 나가는 것을 막는 방어가 이 채널에는 효과가 없다", kind)
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("채널 %s 이(가) 자격증명 키를 선언하지 않았다", kind)
		}
	}
}

func TestSecretKeysDeclaredForEveryKind(t *testing.T) {
	// 컴파일러가 이미 모든 채널에 구현을 강제하지만, 여기서 "마스킹에 대해 백지를 제출한 채널이 없음"을 한 번 더 확인한다. 확인 대상 메서드：SecretKeys
	// 빈 슬라이스를 반환하는 채널은 자격증명이 평문으로 브라우저에 노출된다는 뜻이다.
	expect := map[string]bool{
		KindDingTalk: true, KindFeishu: true, KindWeCom: true,
		KindWebhook: true, KindTelegram: true, KindEmail: true,
	}
	for kind, ch := range registry {
		if !expect[kind] {
			t.Errorf("채널 %s 의 마스킹 기대값이 테스트에 등록되지 않았다", kind)
			continue
		}
		if len(ch.SecretKeys()) == 0 {
			t.Errorf("채널 %s 이(가) 자격증명 필드를 하나도 선언하지 않아, 설정이 평문으로 노출된다", kind)
		}
	}
}

// TestPrepareConfigUpdateRejectsMaskedInContainer 는 감사에서 지적된 틈 하나를 다룬다:
// 마스킹 표식을 **문자열이 아닌** 구조(예: webhook.headers는 객체)에 넣으면,
// MergeConfig는 "문자열이면서 접두사가 붙은 것"만 마스킹으로 인식하므로 리터럴
// "__masked__"가 실제 헤더 값으로 DB에 저장된다——이후 인증이 조용히 무효화되고
// 어떤 에러도 나지 않는다.
func TestPrepareConfigUpdateRejectsMaskedInContainer(t *testing.T) {
	stored := map[string]any{
		"url":     "https://legit.example.com/hook",
		"headers": map[string]any{"Authorization": "Bearer REAL"},
	}
	// 객체 내부에 마스킹 표식을 끼워 넣음.
	incoming := map[string]any{
		"headers": map[string]any{"Authorization": MaskedPrefix},
	}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, incoming); err == nil {
		t.Fatal("구조체 내부에 끼워 넣은 마스킹 표식은 거부되어야 한다(그러지 않으면 리터럴이 DB에 저장된다)")
	}
	// 객체 전체를 제출(실제 새 값)하는 것은 평소처럼 허용한다.
	ok := map[string]any{"headers": map[string]any{"Authorization": "Bearer NEW"}}
	if _, err := PrepareConfigUpdate(KindWebhook, stored, ok); err != nil {
		t.Fatalf("새 요청 헤더를 정상적으로 제출하는 것은 막히면 안 된다: %v", err)
	}
}
