package notify

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 이 파일은 범용 Webhook 템플릿의 **능력 범위**를 고정한다.
//
// 이곳은 패키지 전체에서 유일하게 「사용자가 제공한 문자열이 코드로 평가되는」
// 지점이므로, 할 수 있는 것과 할 수 없는 것을 명확히 정하고 테스트로 그 성질을
// 못 박아 둬야 한다——그러지 않으면 누군가 템플릿 컨텍스트에 아무렇지 않게 메서드를
// 추가하거나 FuncMap에 readFile 같은 것을 추가해도, 능력 범위가 조용히 넓어지는데
// diff만 보면 그저 무해한 작은 함수 하나로 보인다.

// TestTemplateContextHasNoMethods 가 가장 중요한 테스트다.
//
// text/template은 export된 메서드를 호출할 수 있다({{.Foo}}는 필드도 메서드도
// 모두 가져올 수 있다). 그러므로 템플릿 컨텍스트가 export 메서드를 가진 타입에
// **조금이라도** 접근할 수 있다면, 그 메서드들을 템플릿 작성자에게 그대로 열어주는
// 셈이다. 이 기능의 컨텍스트는 의도적으로 순수 데이터만 담는다(export 필드만
// 있고 메서드는 0개).
//
// 이 테스트가 실패한다면: 누군가 webhookTemplateData / webhookItem에 메서드를
// 추가했다는 뜻이다. 허용하기 전에, 그 메서드가 노출하고 싶지 않은 것을 템플릿이
// 읽어내는 데 쓰일 수 있는지 먼저 따져봐야 한다.
func TestTemplateContextHasNoMethods(t *testing.T) {
	for _, v := range []any{webhookTemplateData{}, webhookItem{}} {
		typ := reflect.TypeOf(v)
		if n := typ.NumMethod(); n != 0 {
			var names []string
			for i := 0; i < n; i++ {
				names = append(names, typ.Method(i).Name)
			}
			t.Fatalf("%s 가 메서드 %d개(%s)를 노출합니다: text/template이 이를 호출할 수 있어, "+
				"그 메서드들의 능력을 템플릿 작성자에게 열어주는 셈입니다", typ.Name(), n, strings.Join(names, ", "))
		}
	}
}

// TestTemplateFuncsAreMinimal 은 템플릿에 노출되는 함수 집합을 고정한다.
//
// FuncMap에 함수가 하나 늘어날 때마다 능력도 하나 늘어난다. 현재는 json / jsons
// 뿐이며, 값을 JSON 조각으로 직렬화하는 역할만 한다——파일을 읽거나, 요청을
// 보내거나, 명령을 실행할 수 없다.
func TestTemplateFuncsAreMinimal(t *testing.T) {
	var got []string
	for name := range webhookTemplateFuncs {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"json", "jsons"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("템플릿 함수 집합이 바뀌었습니다: 얻은 값 %v, 기대값 %v. 함수를 추가하기 전에 "+
			"능력 범위가 넓어지지 않는지(파일 읽기/쓰기, 네트워크 요청, 명령 실행이 불가능한지) 확인하세요", got, want)
	}
}

// TestTemplateCannotReachUnknownData 는 템플릿에서의 범위 밖 접근을 다룬다:
// 존재하지 않는 것에 접근하면 반드시 실패해야 하며 아무것도 얼버무려 돌려주면
// 안 되고, 실패 메시지에 내부 데이터가 섞여 나와서도 안 된다.
func TestTemplateCannotReachUnknownData(t *testing.T) {
	_, err := renderWebhookBody(`{"x": {{.Environment}}, "y": {{.Env}}}`, singleMsg())
	if err == nil {
		t.Fatal("존재하지 않는 필드에 접근하면 에러가 나야 합니다")
	}
	// 에러에 템플릿 컨텍스트의 실제 내용(취약점 제목/요약)이 나타나면 안 된다.
	for _, leak := range []string{"SQL 인젝션", "파라미터 id"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("템플릿 에러가 메시지 내용 %q 를 유출했습니다: %v", leak, err)
		}
	}
}

// TestTemplateRenderFailsPermanently 템플릿을 잘못 작성한 것은 설정 오류이므로
// 재시도로 저절로 고쳐지지 않는다. 재시도 가능으로 판정되면, 잘못된 템플릿 하나가
// 매번 전달 시도마다 3라운드 백오프를 헛돌게 만든다.
func TestTemplateRenderFailsPermanently(t *testing.T) {
	cfg := map[string]any{
		"url":           "https://example.com/hook",
		"body_template": `{{.Items.`,
	}
	if err := (webhookChannel{}).Validate(cfg); err == nil {
		t.Fatal("템플릿 문법 오류는 저장 시점에 막혀야 합니다")
	}
	// 검증을 우회해 바로 전달해도, 반복 재시도가 아니라 영구 실패로 판정되어야 한다.
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("잘못된 템플릿은 영구 실패로 판정되어야 하는데 %v 를 얻음", err)
	}
}

// TestTemplateCanOnlyProduceJSON 은 「템플릿 렌더링 결과는 반드시 올바른 JSON이어야
// 한다」는 제약을 다룬다. 이는 동시에 「템플릿으로 순수 텍스트를 만들어 다른
// 프로토콜을 트리거」하는 식의 사용법도 막아준다.
func TestTemplateCanOnlyProduceJSON(t *testing.T) {
	// 올바른 템플릿은 통과해야 한다.
	ok := map[string]any{"url": "https://example.com/hook", "body_template": `{"t":{{json .Title}}}`}
	if err := (webhookChannel{}).Validate(ok); err != nil {
		t.Fatalf("올바른 템플릿은 검증을 통과해야 하는데: %v", err)
	}
	// JSON이 아닌 결과를 내면 반드시 거부해야 한다(그대로 보내버리면 안 된다).
	bad := map[string]any{"url": "http://127.0.0.1:1/hook", "body_template": `not json {{.Count}}`}
	_, err := (webhookChannel{}).Send(context.Background(), bad, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("JSON이 아닌 결과는 영구 실패로 판정되어야 하는데 %v 를 얻음", err)
	}
	if !strings.Contains(err.Error(), "올바른 JSON") {
		t.Errorf("에러 메시지가 JSON 문제임을 설명해야 하는데 %v 를 얻음", err)
	}
}
