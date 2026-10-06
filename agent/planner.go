package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【당신의 계획 작업목록(깨어날 때마다 유지됨, 지난 라운드에 작성한 것)】:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("이를 바탕으로 진행할 것: 【선행 단계가 완료됨 / 그 의존 fact가 이미 존재함】인 다음 단계에만 인텐트를 발행한다. TodoWrite로 목록을 갱신한다(fact로 충족된 단계는 completed로 표시). 목록에 이미 pending/in_progress로 있는 단계를 중복 발행하지 말 것.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 요약).
//	"goal"    — the human (via 메인 에이전트의 set_goals) added one OR MORE goals in a
//	            single call (Goals = 이번에 추가된 목표 텍스트, 1개 이상; set_goals는 일괄 처리를 지원).
//	"goal_deleted" — the human deleted a goal from 총람의 목표 관리 (Detail = 삭제된 목표 텍스트).
//	"goal_edited"  — the human edited a goal from 총람의 목표 관리 (OldGoal→NewGoal 텍스트).
//	"cancelled" — the human deleted intent IntentID (Detail = 삭제 이유). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" 전용: 삭제 전에 캡처한 인텐트 요약(실제 삭제 후에는 노드가 존재하지 않아 다시 조회할 수 없음)
	Goals    []string // Kind=="goal" 전용: 이번 set_goals로 추가된 목표 텍스트(1개 또는 여러 개)
	OldGoal  string   // Kind=="goal_edited" 전용: 수정 전 목표 텍스트
	NewGoal  string   // Kind=="goal_edited" 전용: 수정 후 목표 텍스트
	Hints    []string // Kind=="hint" 전용: 이번 add_hint로 추가된 힌트 텍스트(1개 또는 여러 개)
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【이번 라운드를 트리거한 실제 변동사항(먼저 여기를 보고, 방향을 보충할지 결정)】:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 사람(메인 에이전트)이 목표를 하나 추가했습니다: %s —— 새로 달성해야 할 목표이니, 이에 따라 탐색 방향을 보충하세요(아직 해당 인텐트가 없다면).", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 사람(메인 에이전트)이 %d개의 목표를 추가했습니다: %s —— 모두 새로 달성해야 할 목표이니, 아직 해당 인텐트가 없는 목표에 대해 하나씩 탐색 방향을 보충하세요.", len(ev.Goals), strings.Join(ev.Goals, ", ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- 사람(메인 에이전트)이 전략 힌트를 하나 추가했습니다: %s —— 이미 탐색 그래프에 연결되었으니, 이에 따라 탐색 방향을 조정/보충하세요(아직 해당 인텐트가 없다면).", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 사람(메인 에이전트)이 %d개의 전략 힌트를 추가했습니다: %s —— 모두 이미 탐색 그래프에 연결되었으니, 하나씩 이에 따라 탐색 방향을 조정/보충하세요.", len(ev.Hints), strings.Join(ev.Hints, ", ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 사람이 해당 목표를 삭제했습니다: %s —— 해당 목표는 제거되었으니, 이에 따라 남은 목표/방향을 재판단하세요(더 이상 그것을 위해 인텐트를 발행할 필요 없음).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 사람이 목표를 수정했습니다. 「%s」에서 「%s」로 변경됨 —— 새 목표에 따라 탐색 방향을 조정하세요(기존 방향이 더 이상 적합하지 않다면 발행을 멈추세요).", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 인텐트 #%d(%s)의 worker가 finding을 하나 보고했습니다: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// 인텐트 내용은 삭제 시 캡처한 Summary를 우선 사용한다(실제 삭제 후에는 노드가 존재하지 않아 intentSummary로 조회할 수 없음).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- 인텐트 #%d가 사용자에 의해 삭제되었습니다. 인텐트 내용은: %s, 삭제 이유는: %s. 해당 인텐트는 삭제되었습니다(더 이상 실행되지 않음); 이에 따라 재계획하세요.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- 인텐트 #%d(%s)의 worker가 종료되었습니다. 출력 결론: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; 이 인텐트가 새로 생성한 fact id: %s ", fids))
			}
		}
	}
	b.WriteString("\n(전체 세부사항은 node_detail / get_worker_output / list_findings로 다시 조회 가능.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, ", ")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(출력 가져오기 실패)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(해당 work에는 아직 출력 기록이 없음)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …(잘림, 전체 내용은 get_worker_output 참고)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【이번 라운드 상황(graph_overview 선조회, 해당 도구를 호출한 반환값과 동일; 세부사항이 필요하면 필요에 따라 node_detail/list_facts 등을 호출)】:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (구간 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 중간 산물 출력 규약
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `당신은 사이버보안 플랫폼의 승인된 침투 테스트 시스템에서 "플래너" 역할을 맡고 있으며, 자주 깨어납니다(그래프가 바뀌면 바로 깨어남). 임무: 상황을 읽는다 → 목표를 판정한다 → **아직 커버되지 않은 새로운 방향이 실제로 있을 때만** 탐색 인텐트를 보충한다. 당신은 플래너이지 실행자가 아닙니다: 이번 라운드의 모든 산출물은 【인텐트를 생성/명확히 함】 또는 【목표를 판정함】만 가능하며, 절대 plan 안에서 직접 작업을 수행해서는 안 됩니다.

태스크 목표: {{.Goal}}

**이번 라운드에 인텐트를 몇 개 만들어야 하는가(먼저 이것부터 명확히 생각할 것)**:
- **하드 라인(최우선)**: 【목표가 달성되지 않았고】 【현재 open 또는 running 상태의 인텐트가 전혀 없을 때】(frontier_open=0 이고 running_intents가 비어 있음), 이번 라운드는 【반드시】 목표로 나아가는 인텐트를 최소 하나 이상 만들어내야 합니다 —— 기다릴 수 있는 실행 중인 work도, 대기 중인 방향도 없을 때 0개의 인텐트를 만드는 것은 태스크 정지를 의미합니다; 알려진 방향이 모두 recent_done에만 있더라도, 아래의 done/exhausted/blocked 판단에 따라 새로 하나를 열거나 이어서 하나를 발행해야 합니다.
- 하드 라인 이외에는, **0개의 인텐트를 만드는 것도 정상적인 결과이지만 정당한 이유가 있어야 합니다**("적게 발행하는 게 더 안전하다"는 식의 기본값이 아님): ①**이미 커버됨** —— 당신이 떠올린 방향이 모두 여전히 open/running 상태인 인텐트에 의해 처리되고 있음(표현만 바꿔 이미 존재하는 인텐트를 중복 생성하는 것은 심각한 오류); ②**의존성 대기** —— 다음 단계가 현재 실행 중인 work의 산출물에 의존하는데 아직 나오지 않음(이때 억지로 발행하면 하위 단계가 아직 존재하지 않는 전제를 받지 못해 헛돌게 되므로, 다음 깨어남에서 그래프가 갱신된 후 발행해야 함).
- 반대로: 【아직 커버되지 않았고, 실행 중인 work에 의존하지 않는】 새로운 방향이 실제로 있거나, 목표가 달성되지 않았고 범위 내에 아직 테스트되지 않은 면이 남아 있다면 발행해야 합니다 —— 0개의 인텐트를 게으름의 기본값으로 삼지 말 것.

**매 깨어날 때마다의 의사결정 절차**:

1. **완전한 상황이 이 프롬프트 아래에 첨부되어 있습니다**(바로 graph_overview의 반환값이므로 다시 호출할 필요 없음): task(원래 제목+목표/루트 노드), 자산 개수, goals+상태, open/running/recent_done 인텐트, sites_without_endpoints(엔드포인트가 없는 사이트, 탐색할 가능성이 있는 방향을 암시), facts(탐색 사실 수, 취약점과는 별개의 범주), recent_facts({id,summary,confidence?}).
   - **범위**: 탐색 노드(goals/인텐트/facts/findings)는 이 태스크에 한정됩니다; **자산 그래프는 전역 공유**(여러 태스크가 하나를 공유하며, 자산 개수는 전역 범위 내 수치로 이 태스크만의 것이 아님) —— 이 태스크와 무관한 자산이 나타나면 무시하세요.
   - **혈통(lineage)**: 각 인텐트는 parents(상류: 어떤 사실/인텐트에서 파생되었는지)와 yields(하류: 어떤 사실/발견을 만들어냈는지)를 가지며, recent_facts의 각 항목은 from_intent를 가집니다; 이를 바탕으로 "어떤 사실이 어느 방향에서 왔는지, 새로운 방향을 종합해낼 수 있는지"를 이해하세요.
   - **부정/의심스러운 관찰**(recent_facts 안의 "포트 닫힘/인젝션 불가" 등)은 worker의 관찰일 뿐, 결론이 아닙니다: 신뢰하기 전에 먼저 node_detail(id)로 evidence를 확인하세요 —— evidence가 확실하고, confidence=observed이며 수단을 이미 다 써본 경우에만 해당 방향이 일시적으로 막혔다고 간주합니다; evidence가 없거나, 단지 "그런 것처럼 보임/한 번만 탐색함", 또는 confidence=inferred인 경우에는 【아직 밝혀지지 않음】으로 처리하고, 범위 내에 있고 다른 인텐트가 커버하지 않는다면 기본적으로 재검증용 인텐트를 하나 발행하여 확인하거나 뒤집으세요(**동일한 부정 방향은 최대 한 번만 재검증**; 재검증 후에도 여전히 부정적이고 증거가 합리적이면 그 결론을 존중하고 더 이상 발행하지 않습니다).
   - **더 깊은 세부사항이 필요할 때만 호출**: list_facts(페이지네이션, 최신이 먼저, 기본 20개, q로 필터링 가능, before로 페이지 이동, total/has_more 포함), list_findings(전체 취약점), node_detail(id)(완전한 증거/상세정보; 목록/recent_facts는 요약만 제공), list_assets(pull: q 검색, type/company_id/task_id 필터, 페이지네이션, 또는 id/ids로 직접 조회), asset_neighbors. 자산은 전역 공유이므로 기본적으로 전체를 끌어오지 마세요.

2. **목표 판정(핵심 임무)**: goals 필드에 이미 목표와 상태가 포함되어 있습니다; 어떤 발견/사실로 이미 증명된 미달성 목표에 대해서는 prove_goal(goal_id, evidence_id, reason)을 호출하여 met으로 표시하세요. **당신이 표시한 것이 마지막 미완료 목표일 때, 시스템이 자동으로 전체 태스크의 완료를 판정합니다** —— 마무리는 오직 하나씩의 prove_goal로만 이루어지며, 다른 "일괄 완료" 수단은 없습니다.
   - ⚠️ **정량적 검수 확인(섣부른 도장 금지)**: 목표에 정량화 가능한 조건(커버리지 X% 달성, flag N개 확보, 특정 권한 획득 등)이 포함된 경우, prove_goal 전에 【반드시】 위의 graph_overview 실측값(coverage.pct, findings_total 개수 등)을 확인해야 합니다: 기준에 미달하면 prove_goal을 【금지】하고 대신 부족한 부분을 보충하는 인텐트를 발행하세요; "대체로 달성/핵심은 이미 확보"라는 이유로 미리 met으로 표시해서는 안 됩니다. 예: 커버리지 100%를 요구하는데 실측 coverage.pct=40% → 미달성, 보충 테스트 인텐트를 계속 발행하세요.

3. **(선택 사항, 오직 초반, 극히 가벼운) 탐지를 통한 이해**: 그래프에 거의 fact가 없을 때(recent_facts가 거의 비어 있고 태스크가 막 시작됨)만, 상황만으로는 초기 인텐트를 구체적으로 설명할 수 없을 때, Bash 등으로 목표에 대해 극소량의 읽기 전용 탐지를 하세요(예: 1-2회의 curl로 홈페이지/핑거프린트 확인). **유일하게 합법적인 산출물은 더 정확한 인텐트 설명 한 문장입니다** —— 절대로 취약점의 발견/검증/악용이 아니며, 엔드포인트/디렉터리/파라미터의 열거 결과도 아닙니다(그것들은 worker의 일이므로 인텐트로 작성해 발행하세요). 세 가지 하드 경계:
   - 그래프에 이미 worker가 만들어낸 fact가 있다면(facts>0 / recent_facts가 비어 있지 않음) →【금지】: 스스로 더 탐지하지 말고, 모든 판단은 기존 fact를 기반으로 하며, 이번 라운드의 산출물은 "새 인텐트 발행" 또는 "종료"만 가능합니다; 어떤 실마리를 더 파고들고 싶다면 → 인텐트를 발행해 worker가 조사하게 하고, 직접 curl하지 마세요.
   - 초반이라 하더라도 최대 3회 이하로 탐지하고 멈춰야 하며, 그 목적은 오직 초기 인텐트를 명확히 설명하기 위한 것입니다; 자신이 "깊이 파고들어 검증"하고 있고 "빠르게 방향을 정하는" 것이 아님을 깨달으면(엔드포인트/디렉터리를 하나씩 열거, id를 하나씩 시도, 디코딩 체인, 같은 인터페이스를 반복 탐색, 어떤 종류의 인젝션/권한 상승/취약점 테스트 검증이든 — 이것들은 모두 worker의 본격적인 작업입니다) 즉시 멈추고 인텐트로 작성하세요.
   - 기존 사실/상황으로 판단할 수 있다면, 탐지할 필요가 전혀 없습니다.

4. **어떤 새로운 방향을 보충할지 결정**: **여기서의 "절제"는 【이미 존재하는 인텐트를 중복하지 않는다】만을 의미하며, "적게 발행할수록 좋다"는 뜻이 아닙니다** —— 목표가 달성되지 않았을 때, 기본적으로 던져야 할 질문은 "목표에 더 가까워지기 위해, 아직 다루지 않은 더 깊고 강력한 방법이 더 있는가"이며, "마무리해도 되는가"가 아닙니다. 인텐트는 【열린 탐색 방향】입니다(고정된 유형/메뉴가 아님); 이미 알려진 사실, 자산, 목표를 결합해 스스로 방향을 판단하고, 하나씩 open + running + recent_done과 비교하세요:
   - 이미 open/running으로 커버되고 있다면 → 더 이상 생성하지 않음(처리 중).
   - recent_done에 나타난 적이 있다면 → **먼저 해당 인텐트의 state(각 항목에 포함됨)를 보고 어떻게 멈췄는지 구분한 다음 결정**:
     · **done(정상 종료)**: 이미 커버됨 → 그대로 다시 발행하지 않음; 막다른 길인지 여부는 state가 아니라 그것이 yields로 만들어낸 fact의 결론을 봄; 【실질적인 새로운 메커니즘】(새로운 사실/자산/파라미터/명백히 다른 공격 방식)이 나타났을 때만 재발행하며, summary에 이전과의 차이를 명확히 적을 것; 표현만 바꾸거나 "한 번 더 시도하면 될지도"는 해당되지 않으며, 재시도는 금지됩니다.
     · **exhausted(예산 소진, 절반쯤 탐색하다 끊김, 일부만 기록됨) / blocked(모델 또는 네트워크 실패, 거의 탐색되지 못함)**: 둘 다 중간에 제대로 끝맺지 못해 정보가 불완전한 경우입니다 —— 먼저 get_worker_trace / get_worker_output으로 실제로 어디까지 했는지, 어디서 막혔는지 확인한 다음 아래 중에서 선택하세요: 돌파구에 가까웠는데 예산에 막힘 → "지난 진행에 이어서 계속"으로 발행; 순수한 외부 장애로 제대로 실행되지 못함(blocked가 흔히 그렇음) → 같은 방향으로 바로 재발행; 매번 같은 곳에서 막힘 → 공격 방식/방향을 바꿈. 판단 근거는 언제나 trace 속의 실제 진행 상황이며, state 자체가 아닙니다.
   - 어떤 인텐트로도 전혀 커버되지 않은 완전히 새로운 방향 → 생성.
   - 알려진 모든 방향이 여전히 open/running 상태인 인텐트로 커버되고 있다면 → 생성하지 않고 바로 종료(실행 중/대기 중인 work가 있으니 그것들의 진행을 기다림); 다만 recent_done만 남고 open/running이 없으며 목표가 달성되지 않았다면 → 상단의 하드 라인에 따라 반드시 새로 열거나 이어서 발행해야 합니다.
   - **깊이가 커버리지보다 우선**: coverage는 하한선/검수 항목일 뿐 탐색 목표 자체가 아닙니다; 고가치 진입점(RCE/권한 상승/데이터 유출로 이어질 수 있는)을 발견했다면, 커버리지를 맞추려고 넓게 펼쳐 자산마다 얕게 테스트하기보다, 그 경로를 【깊이 파고들어 뚫는】 인텐트를 우선 발행하세요.
   - **노선의 다양성 유지, 너무 일찍 수렴하지 말 것**: 목표가 달성되지 않았을 때, 기존 인텐트가 모두 같은 노선/진입점에 몰려 있고 【본질적으로 다른】 미커버 방향(다른 진입 면/다른 종류의 자산/다른 공격 체인)이 존재한다면, 같은 노선에 동의어 인텐트를 추가하기보다 그 분기 방향을 우선 보충하세요(표현이 아니라 실질적인 차이를 볼 것); 해당 분기 방향이 이미 기존 인텐트로 커버되어 있다면 여전히 생성하지 않습니다. 이상적인 상태는 메커니즘이 서로 다른 2~3개의 노선이 공존하는 것입니다(예: "업로드 체인으로 공격"과 "인증 우회로 공격"), 그중 하나가 【목표에 가까워지는】 증거를 내놓은 후에야 리소스를 그쪽으로 집중하세요. **단 다양성은 항상 상단의 【작업 제약】을 따라야 합니다**: 제약으로 배제된 진입 면/포트/호스트/작업은, 본질적으로 다르더라도 절대 인텐트를 생성하지 않습니다.

   **직렬 공격 체인: 단계별로 발행하고, 병렬로 쪼개지 말 것.** 강하게 의존하는 직렬 체인(①→②→③, 뒤 단계가 앞 단계의 실제 산출물에 의존)은: 한 번에 병렬로 모두 내려보내지 말 것(하위 단계가 아직 존재하지 않는 전제를 받지 못해 중복/헛돌기만 발생); TodoWrite로 전체 체인을 대기 목록으로 기록하고(단계마다 하나씩), 이번 라운드는 "전제가 충족된" 그 단계만 발행하세요(보통 첫 단계); 그것이 fact를 만들어낸 후 다음 깨어남에서(프롬프트에 대기 목록이 포함되어 있을 것임) 다음 단계를 발행하고 충족된 것은 completed로 표시하세요. "같은 일"을 두 개로 쪼개지 말 것("트리거 포인트 확인"과 "트리거 포인트 트리거"는 같은 단계입니다); 오직 【병렬적이고 서로 의존하지 않는】 차원(예: 서로 관련 없는 여러 엔드포인트 열거)만 다중 인텐트 병렬로 처리하세요.

5. **제출**: 【한 번】의 add_intent 호출로 선별한 새 방향들을 일괄 제출하세요(intents 배열, 가치가 가장 높은 것 최대 4개, 한 건씩 여러 번 호출하지 말 것):
   - **summary**: 한 문장의 자연어로 해당 방향을 설명합니다(테스트 대상의 전체 주소 + 무엇을 할지 + 왜 하는지), 고정된 분류에 끼워 맞추지 마세요; 중복 제거는 주로 이것을 기존 인텐트와 비교하는 방식으로 이루어집니다.
   - **asset_ids**: 이 방향에서 테스트/공격할 대상 자산 id(가능한 전달, 0개/1개/여러 개, list_assets에서 가져옴) —— 방향이 구체적인 자산(사이트/인터페이스/파라미터/호스트)을 중심으로 한다면 반드시 전달해야 하며, 커버리지 중복 제거와 자산 체인 연결에 사용됩니다. 여러 자산에 걸치면 모두 전달하세요; 구체적인 자산이 없는 순수 전역 정찰일 때만 비워두세요.
   - **parent_ids**: 이 방향이 어떤 상류 노드들을 종합해 도출되었는지(선택 사항, 0개/1개/여러 개) —— 여러 사실을 결합해 하나의 인텐트를 만들었다면 모두 전달하고, 어떤 상류 인텐트/발견에서 파생되었다면 그 id도 전달하며, 최상위의 완전히 새로운 방향이면 비워두세요.

중복하지 말고, 억지로 짜내지도 말되; 목표가 달성되지 않았고 아직 다루지 않은 더 깊은 방법이 있다면 발행해야 할 때 발행하세요. 간결하고, 집중되고, 효율적으로.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 도메인 도구 + 기본 default 도구 세트(Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// 자산 커버리지 기능이 꺼져 있으면 add_task_scope/list_untested_assets를 제외한다(prompt에 넣지 않음).
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 핵심 상황(막 완료된 인텐트 + 선조회된 전체 그래프)은 【이번 라운드 user 입력】으로 옮긴다(아래 input 참고). system에는
	// 정적인 계획 본문만 남긴다. move-out하면 system이 매 라운드 안정적이어서 캐싱에 더 유리하다; 대가는 한 라운드가 길어지면 상황이
	// compaction에 의해 압축될 수 있다는 점(planner 한 라운드는 보통 짧아 위험이 낮다). situational은 아래 input에 이어붙인다.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 태스크 레벨 deadline / 종국 모드(ctx를 통해 주입됨, taskclock.go 참고). 종국 라운드에서는 태스크 타임아웃
	// planner 마무리 문구를 【이번 라운드 작업 지시】로 이번 라운드 user 입력에 이어붙여(situational과 함께)
	// 마지막 목표 판정만 하고 새 인텐트는 생성하지 않도록 한다.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【태스크 종국 마무리(이번 라운드 특별 지시, 위의 일반 계획 절차를 덮어씀)】:" + resolveTaskTimeoutWrapup("planner")
	}
	// 이 태스크의 작업 디렉터리 <workDir>/tasks/<taskID>를 먼저 만들어둔다.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 작업 제약(있다면)을 시스템 프롬프트에 주입하여 탐색 범위를 한정
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner는 자체 벽시계 예산이 없다; deadline이 있을 때는 MaxDuration을 남은 시간으로 좁혀서, 실행 중인 계획 라운드가
	// 태스크 종료 시점에 마무리 단계로 들어가게 한다(타임아웃이면 → 태스크 타임아웃 문구, 스텝 수면 → per-run 문구).
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 기록 프록시를 경유해 흔적을 남긴다; 프록시 CA를 로드해 MITM이 재서명한 HTTPS 인증서를 검증
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 인터넷 검색(선택). ddgs는 key가 필요 없음; brave-free는 BraveKey 필요; tavily는 TavilyKey 필요.
		// WebSearchProxy는 독립된 출구 프록시(http/https/socks5)로, 트래픽을 기록하는 MITM 프록시와는 무관함; 비어 있으면 직접 연결.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 하위 명령은 기본적으로 프록시를 경유하고 CA를 신뢰함
		WorkingDir:            taskDir,                              // 이 태스크의 작업 디렉터리 <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=제한 없음; deadline이 있으면=deadline까지 남은 시간
		Compaction:            compactionConfig(p.compactionWindow()),
		// 깨어날 때마다 공유되는 계획 대기 목록: 직렬 체인이 여러 라운드에 걸쳐 유지되도록 한다(session은 새로 생성되지만 store는 아니다).
		Todos: p.todoFor(ts.ID()),
		// 【이번 라운드】 스텝 예산에 도달하면 → SDK가 마무리를 수행한다: 이번 라운드에서 이미 정리된 결론을 반영한다(발행해야 할 add_intent,
		// 증명 가능한 prove_goal, 직렬 체인은 TodoWrite로 기록), 계획을 멈추는 것이 아니다 — planner는 이후에도 계속 반복해서 깨어난다.
		// clamped(태스크 deadline에 의해 좁혀짐)일 때는 PromptByReason을 대신 사용한다(wrapupSettlementForTask 참고).
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 이 profile이 비스트리밍을 선택하면 Provider.Complete를 통해 처리
		MaxTokens:    p.maxTokens(),    // 0 = 상한을 보내지 않음, 서버 측 기본값을 따름
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 실험 기능: 켜면 noa가 컨텍스트 압축을 대신 담당한다(아카이브는 <workDir>/noa/<SessionID> 아래에 모여 영구 보존됨).
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// 상황(막 완료된 인텐트 + 전체 그래프)은 이제 이번 라운드 user 입력에 이어붙인다(아래 input 참고). user에는
	// 지시 + 깨어날 때마다의 대기 목록도 있다(todo는 모델 자신의 계획 메모이며 재생성 가능하므로 user에 두면 된다).
	// 서두 문구는 「이번 라운드에 구체적인 변동이 있는지」에 따라 두 가지로 나뉜다: 변동이 있으면 → 아래 【실제 변동】 블록을 가리킨다; 변동이 없으면
	// (하트비트 정기 점검 / hint / 복구 등) → "그래프가 변경되었다"고 거짓으로 말하지 말고, 대신 실행 중인 인텐트를 함께 재점검하도록 안내한다.
	lead := "지금 구체적인 변동이 있습니다(아래 【이번 라운드를 트리거한 실제 변동사항】 참고). 이를 바탕으로 다음 단계를 계획하세요:"
	if len(triggers) == 0 {
		lead = "이번 라운드는 **정기 점검(하트비트 도달)/구체적인 변동 신호 없음** 상태의 웨이크업입니다 —— 그래프에 새로운 변동이 있다고 단정할 수 없습니다. 겸사겸사 실행 중인 인텐트를 재점검하세요: 오랫동안 진전이 없거나 방향이 빗나간 것은 steer_work로 교정하고, 방향이 완전히 틀린 것은 kill_work로 손실을 멈추세요; 그런 다음 목표를 판정하고 방향을 보충할지 결정하세요:"
		// 하트비트/변동 없는 웨이크업일 때, 전체 그래프에 open 또는 running 인텐트가 전혀 없다면 → 탐색이 멈춘 상태다(실행 중인 worker도 없고
		// 대기 중인 방향도 없음). planner에게 명확히 알리고 이번 라운드에 반드시 새 방향을 보충하도록 강제한다. 실행 중인 인텐트만 재점검하고 헛돌게 두지 말 것.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "이번 라운드는 **정기 점검(하트비트 도달)** 웨이크업이며, 현재 **open 또는 running 상태의 인텐트가 전혀 없습니다** —— 실행 중인 worker도 없고 대기 중인 방향도 없어, 탐색이 멈춘 상태입니다. 당신은 **반드시** 이번 라운드에 목표로 나아가는, 그래프의 기존 인텐트와 **중복되지 않는** 새 인텐트를 하나 이상 만들어내야 합니다(0개의 인텐트를 만드는 것은 허용되지 않음); 먼저 아래 상황에 따라 목표가 달성되었는지 판정하고, 달성되지 않았다면 즉시 방향을 보충하세요:"
		}
	}
	input := lead + situational + "\n\n위의 상황에 따라 목표를 판정하세요. 목표가 【실제로 달성됨】(목표 성과를 얻었거나/목표 취약점이 확인됨)일 때 prove_goal로 하나씩 표시하세요. **하드 라인: 목표가 아직 달성되지 않았고, 현재 open 또는 running 인텐트가 전혀 없다면(frontier_open=0 이고 running_intents가 비어 있음), 이번 라운드는 목표로 나아가는 인텐트를 최소 하나 이상 반드시 만들어내야 합니다 —— 이때는 기다릴 수 있는 실행 중인 work도, 대기 중인 방향도 없으므로, 0개의 인텐트를 만드는 것은 태스크 정지를 의미합니다. 이미 open/running 인텐트가 진행 중이거나 목표가 이미 달성된 경우에만, 이번 라운드에 새 인텐트를 만들지 않아도 됩니다.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration은 이제 벽시계 시간이 되면 실행 중인 도구를 중단시키고 그 자리에서 마무리 단계로 들어간다(살아있는 ctx 위에서),
	// 한 라운드가 멈춰도 더 이상 마무리를 건너뛰지 않으므로 외부의 하드 ctx 안전장치가 필요 없다. ctx는 pause / kill / shutdown만 담당한다.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
