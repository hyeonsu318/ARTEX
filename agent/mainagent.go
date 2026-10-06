package agent

import (
	"context"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (섹션 [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the 중간
// 산출물 출력 규약 tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `당신은 인가된 침투 테스트 시스템의 "메인 agent"이며, 사람 운영자와의 인터페이스입니다. 당신은 직접 탐색하지도, 의도를 자율적으로 연속 생성하지도 않습니다(그것은 플래너의 일입니다). 당신의 역할은 다음과 같습니다:

1. 관찰: graph_overview / list_findings / list_facts / list_assets / get_worker_output으로 현재 진행 상황에 대한 사람의 질문에 답하세요.
2. 조타(사람의 의도를 시스템에 반영):
   - 사람이 "방향 변경/특정 유형의 취약점 강조/특정 영역 집중"을 원할 때 → add_hint로 힌트를 작성(플래너가 다음에 읽게 됨).
   - 사람이 "특정 대상을 즉시 테스트"하길 원할 때 → add_intent로 우선순위가 높은 의도를 직접 주입(priority 8-10). 시스템이 완료된 작업을 자동으로 실행 상태로 되돌려 worker가 이 의도를 할당받아 실행하게 하며, 실행이 끝나면 곧바로 완료 상태로 돌아갑니다.
     **작업 목표가 모두 달성된 경우**(graph_overview에서 goals가 모두 met): 내리기 전에 이 의도 뒤에 "새로 달성해야 할 결과"가 암시되어 있는지 먼저 판단하세요. 암시되어 있다면 짐작한 목표를 한 문장으로 사람에게 다시 말해 주고, **정식 목표로 등록할지 되물으세요**——원하면 → set_goals로 등록(이후 작업이 일반 플래닝에 들어가며 플래너가 자율적으로 계속 진행); 원하지 않거나 / 잠깐 확인만 해 보고 싶은 것이면 → add_intent로 이 한 건만 내리고, worker가 실행을 마치면 작업은 곧바로 완료 상태로 돌아갑니다(자율적으로 계속하지 않음). 이 의도가 분명히 일회성 확인일 뿐 새로운 목표를 암시하지 않는다면 곧바로 add_intent만 하면 되며, 매번 물을 필요는 없습니다.
   - 사람이 "실행 중인 의도(work)를 실시간으로 교정(X는 더 이상 하지 말고 Y에 집중)"하길 원할 때 → steer_work 사용(중단하지 않고 기존 진행 상황을 잃지 않으며, worker의 다음 동작 이전에 적용됨); 먼저 get_worker_output으로 무엇을 하고 있는지 확인하세요. 방향이 완전히 틀렸다면 add_intent로 새 의도를 따로 내리세요.
   - 사람이 "달성해야 할 최종 목표를 새로 추가"하길 원할 때 → set_goals로 목표를 보강하세요. 시스템이 해당 목표를 작업 그래프에 기록하고 **완료/일시중지된 작업을 자동으로 실행 상태로 되돌려 계속 진행**하며(플래너가 이후 이를 바탕으로 달성 여부를 다시 판단), 사람이 따로 재개를 누를 필요가 없습니다.
   - 사람이 "테스트 제약을 추가/변경(특정 유형의 작업 허용/금지, 예: 『현재 포트만 테스트』『브루트포스 금지』『수동 정찰만 수행』)"하길 원할 때 → set_constraints로 등록(type=allow 허용 / type=deny 금지). 제약은 다음 플래닝 라운드에서 planner/worker의 프롬프트에 주입되어 탐색 경계를 정하며, 개요의 「제약 관리」에서 추가/삭제/수정할 수도 있습니다.
3. 사람이 알아듣는 말로 간결하게 답하고, 무엇을 했는지 설명하세요.

현재 작업 목표: {{.Goal}}

발견 사항을 지어내지 마세요. 도구가 반환한 실제 데이터에 근거해서만 답하세요.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // 범용 깨우기(전용 콜백이 없는 쓰기 작업이 이를 사용, debounced)
	tsx.SetResumeTask(resume)     // set_goals로 목표 추가 → 완료/일시중지된 작업을 running으로 되돌림
	tsx.SetNotifyGoal(notifyGoal) // set_goals로 목표 추가 → planner에게 「사람이 목표를 추가함: …」 트리거를 하나 기록
	tsx.SetNotifyHint(notifyHint) // add_hint로 힌트 추가 → planner에게 「사람이 N개의 전략 힌트를 추가함: …」 트리거를 하나 기록
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// 도메인 도구 + 기본 기본 도구 세트(Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 커버리지 기능이 꺼져 있으면 add_task_scope/list_untested_assets를 제외(prompt에 넣지 않음).
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// 이 작업의 작업 디렉터리 <workDir>/tasks/<taskID>를 먼저 만들어 둔다.
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 기록 프록시를 거쳐 흔적을 남김; 프록시 CA를 로드해 MITM이 재서명한 HTTPS 인증서를 검증
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// 웹 검색(선택). ddgs는 key 불필요; brave-free는 BraveKey 필요; tavily는 TavilyKey 필요.
		// WebSearchProxy는 독립된 외부 출구 프록시(http/https/socks5)로, 트래픽을 기록하는 MITM 프록시와 무관하며; 비어 있으면 직접 연결.
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // Bash 하위 명령은 기본적으로 프록시 사용 + CA 신뢰
		WorkingDir:            mainDir,                              // 이 작업의 작업 디렉터리 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // 세션 단위 임시 할 일(TodoWrite), 순수 플래닝용이며 종료하면 사라짐
		// 예산(스텝 수)에 도달하면 → SDK가 마무리를 수행: 사용자에게 진행 상황 한 문장 요약을 출력. Prompt와 마무리 턴 수는 백오피스에서 편집 가능(기본 10턴).
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // 해당 profile이 비스트리밍을 선택하면 Provider.Complete 사용
		MaxTokens:    m.maxTokens(),    // 0 = 상한을 보내지 않음, 서버 기본값이 결정
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// 실험 기능: 켜면 noa가 컨텍스트 압축을 담당(아카이브는 <workDir>/noa/<SessionID> 아래에 모아 영구 보관).
	// session id는 transcript와 같은 규칙(세그먼트 인식)을 따라, 아카이브와 복구가 정렬되도록 한다.
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
