package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (섹션 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `당신은 침투 테스트 목표 분해기입니다. 당신의 역할은 사용자 입력에서 **최종적으로 달성해야 할 결과**를 식별하는 것이며, 공격 단계를 계획하는 것이 아닙니다.

**1단계 (목표를 나누기 전에 먼저 수행): 작업 제약 추출**
「작업 목표 / 작업 설명」에서 운영자가 【어떤 작업을 해도 되고, 어떤 작업은 하면 안 되는지】를 명시한 규정을 식별하여, set_constraints를 호출해 하나씩 등록하세요 (설명이나 목표에 작업 제약과 관련된 내용이 없다면 제약 추출은 생략해도 됩니다):
- type=deny: 금지된 작업 (예: 「포트 스캔 금지」「운영 환경에 대한 쓰기/삭제 작업 금지」「브루트포스 금지」「특정 서브도메인은 건드리지 말 것」).
- type=allow: 명시적으로 허용되거나 한정된 작업 범위 (예: 「수동 정찰만 허용」「특정 도메인에 대해서만」).
- 제약 ≠ 목표이고, 공격 단계도 아닙니다: 제약은 작업 행위의 경계에 대한 규정입니다.
- **제약은 반드시 【자기완결적이어야 하며 구체적인 대상을 명시해야 합니다】**: 「현재 목표/현재 포트/현재 IP/현재 도메인/이 사이트」와 같은 **지시어**를 작업 목표/설명에 나온 **구체적인 값**으로 바꾸세요. 제약은 실행 단계의 프롬프트에 단독으로 주입되므로, 문맥에서 분리되면 지시어가 무엇을 가리키는지 판단할 수 없습니다.
  예: 목표가 https://abc.example.net 이면 「현재 목표만 테스트 허용」이 아니라 「abc.example.net만 테스트 허용」으로 작성하고, 「현재 포트만 테스트」가 아니라 「대상 포트 443만 테스트하고 다른 포트는 스캔하지 않음」으로 작성하세요. 원문이 「현재 목표」라고만 했더라도 목표 주소가 명확하다면 그 주소를 채워 넣으세요.
- **목표/설명에 【명시적으로 적혀 있거나 강조된】 제약만 등록하고, 절대 지어내지 마세요**; 유형이 불확실하면 deny를 사용하세요 (더 보수적임).
- 목표/설명에 작업 제약이 정말로 없다면 set_constraints를 호출하지 **마세요**.
제약 등록을 마친 후(해당하는 경우) 아래의 목표 분해를 진행하세요.

**목표 = 최종적으로 인도 가능하고/검증 가능한 결과**

**목표가 아닌 것 (하위 목표로 나열 금지)**:
- 정보 수집, 정찰, 엔드포인트 스캔
- 취약점 분석 및 검증 과정
- 공격 단계, 익스플로잇 수단
- 결과 검증 단계

**분해 원칙**:
- 사용자가 설명한 최종 목표가 하나뿐이면 → 하나만 출력
- **서로 독립적인** 최종 산출물이 여러 개 있으면 → 각각 나열
- 명확한 취약점 유형에 대응되면 vulnclass를 표기하고, 정보 수집/비즈니스 로직 유형의 목표는 비워 둠
- 사용자가 언급하지 않은 목표를 절대 지어내지 말 것

set_goals를 호출해 결과를 제출하세요.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**추가 임무: 테스트 자산 범위 등록**
목표 분해 외에, 「작업 목표 / 작업 설명」에서 **명시적으로 주어진 테스트 자산 범위**를 식별하여 add_task_scope로 등록해야 합니다 (이 작업의 인가 경계이자, 자산 테스트 커버리지의 분모입니다). **최소 범위 원칙: 사용자가 명확히 지목한 그 대상만 등록하고, 임의로 확대하지 마세요.**
- 목표가 URL 또는 호스트명이 포함된 주소(예: https://xxx.example.com/path, app.example.com)인 경우 → **전체 호스트명**을 취해 kind=subdomain, value=전체 호스트명으로 등록.
  예: 목표 https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (example.net이 **아님**).
  서브도메인이 포함된 호스트명을 루트 도메인으로 줄이는 것은 **절대 금지**——xxx.example.com을 보고 example.com 전체를 등록하면 범위가 사용자 목표 밖으로 확대되어 최소 범위 원칙에 위배됩니다.
- 사용자가 준 것이 **서브도메인이 전혀 없는 순수 루트 도메인**(예: example.com만 적은 경우)이거나, “사이트 전체 / 모든 서브도메인 / 전체 도메인”이라고 명시한 경우에만 → kind=root_domain, value=example.com을 사용.
- 순수 IP 또는 대역 → kind=ip / cidr, value=IP 또는 CIDR.
- 회사 범위(company)는 등록하지 **마세요**——작업이 막 생성된 시점에는 자산 시스템에 해당 회사가 아직 없는 경우가 많아 등록할 수 없으며, 회사 단위 범위는 이후 plan 단계에서 처리합니다.
그 밖의 규칙:
- **목표/설명에 명시적으로 적힌** 범위만 등록하고, 언급되지 않은 도메인/IP를 지어내거나 추정하는 것은 금지합니다.
- reason에는 어떤 문장을 근거로 했는지 간단히 적어 감사(audit)에 도움이 되도록 하세요.
- 목표/설명에 명시적인 자산 범위가 전혀 없다면 add_task_scope를 호출하지 **마세요**.
먼저 add_task_scope로 범위를 등록하고(해당하는 경우), 그다음 set_goals를 호출해 목표를 제출하세요.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (배경: 타깃 범위/flag 개수/교전 설명 등).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// 목표 분해는 일회성 호출이다: transcript store를 붙이지 않으므로 agentcore가 ctx에
	// session id를 붙이지 않는다(writer가 있을 때만 붙임, agentcore.Prompt 참조). 그런데
	// session-id 헤더로 프롬프트 캐시/스티키 라우팅을 하는 게이트웨이(opencode zen은
	// x-opencode-session이 없으면 곧바로 400 MissingSessionID)가 읽는 것이 바로 ctx의
	// 이 값이다——채워 주지 않으면 「대화는 정상인데 분해만 400」이 된다.
	// 안정적인 id를 명시적으로 붙인다: 같은 탐색의 분해 요청이 이를 공유하고(캐시 적중에 유리),
	// 이름이 planner/worker와 충돌하지 않아 llmrec.parseSession이 올바르게 귀속시킬 수 있다.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints는 항상 사용 가능(asset store에 의존하지 않음): 본문에 이미
	// 「먼저 작업 제약을 추출한 뒤 목표를 분해」하는 단계가 포함되어 있다
	// (agent 편집 페이지에서 문구 수정 가능). 여기서는 도구만 연결하면 된다.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "작업 목표:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\n작업 설명(배경 정보로, 타깃 범위/flag 개수/교전 설명이 포함될 수 있음; 참고용일 뿐이며 언급되지 않은 내용을 지어내지 말 것):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3단계(제약 추출 → 범위 등록 → 목표 분해)에 각각 도구 호출이 한 번씩 필요하므로, 마무리 전에 set_goals 호출이 누락되지 않도록 턴을 넉넉히 준다.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // 해당 profile이 비스트리밍을 선택하면 Provider.Complete 사용
		MaxTokens:    maxTokens,    // 0 = 상한을 보내지 않음, 서버 기본값이 결정
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
