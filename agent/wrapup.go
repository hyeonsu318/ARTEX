package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 마무리 프롬프트(wrap-up / settlement prompt): agent가 "스텝 소진(MaxTurns)" 또는
// "타임아웃(run_seconds/MaxDuration)"으로 종료될 때, SDK의 settlement 단계가 이
// 프롬프트를 주입해 agent가 먼저 식별했지만 아직 기록하지 않은 내용을 DB에 반영하고,
// 한 줄 요약을 출력하게 하여 마무리가 흐지부지되는 것을 막는다.
//
// 각 agent의 마무리 프롬프트는 백오피스에서 필요에 따라 덮어쓸 수 있고(agents.wrapup_prompt에
// 저장), 비워두면 여기의 내장 기본값을 쓴다. "프롬프트 본문"만 편집 가능하며, 어떤
// 도구를 비활성화할지와 마무리 자체에 몇 턴의 예산을 줄지는 코드에 고정된 정책이다.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// 내장 기본 마무리 프롬프트, agent key로 색인. worker는 과거에 하드코딩됐던
// settleWrapUpPrompt(worker.go에 정의)를 재사용하고, planner/mainagent는 각각 별도
// 버전을 갖는다. 매칭되지 않는 경우(커스텀 agent)는 범용 기본값으로 처리한다.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: 각 agent의 마무리 단계 "자체" 턴 예산의 내장 기본값(백오피스에서
// >0 값으로 덮어쓸 수 있음). 모두 10턴을 주어 마무리 단계에 DB 반영할 충분한 스텝을
// 보장한다. 매칭되지 않으면 genericWrapupTurns로 처리한다.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "이번 라운드의 계획 스텝이 곧 소진됩니다 — 이는 단지 '이번 라운드'가 끝나는 것일 뿐, 시스템은 이후에도 상황 변화에 따라 당신을 다시 깨워 계획을 이어가게 할 것이며 작업이 종료되는 것이 아니므로, 지금 전체 계획을 마무리할 필요는 없습니다. 이번 라운드에서 이미 정리된 결론을 반영하고 이번 라운드가 헛되지 않게 하되, '마무리를 위해 억지로 의도를 만들어내지'는 마십시오(이번 라운드에 의도가 0개인 것도 완전히 정상적인 결과입니다): (1) '지금 바로 파견해야 할' 탐색 방향을 이미 판단했다면 add_intent를 한 번에 일괄 제출하십시오(생각해둔 것을 묵혀두지 말고 제출하십시오); (2) 어떤 발견/사실로 이미 달성이 증명된 목표가 있다면 prove_goal을 호출해 met으로 표시하십시오(빠뜨리지 마십시오); (3) 단계별로 나눠야 하는 연속 공격 체인을 식별했다면 TodoWrite로 기록해, 다음에 깨어났을 때 이어서 파견할 수 있게 하십시오. 끝나면 바로 이번 라운드를 종료하면 되고, 요약 텍스트를 출력할 필요는 없습니다."

const mainAgentWrapUpDefault = "당신의 스텝이 곧 소진되어 이번 상호작용이 끝나려 합니다. 더 이상 새로운 탐색/작업을 시작하지 마십시오. **순수 텍스트 한 문장으로만** 사용자에게 현재 진행 상황, 핵심 결론, 그리고 권장하는 다음 단계를 요약해 주십시오."

const genericWrapUpDefault = "예산 소진으로 곧 종료됩니다. 먼저 이미 완료했지만 아직 DB에 반영하지 않은 결과를 기록한 뒤, **순수 텍스트 한 문장으로만** 당신이 무엇을 했고 어떤 핵심 결론을 얻었는지 요약해 주십시오(이 문장이 이번 실행의 결과로 표시됩니다)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- 작업 단위 타임아웃 마무리 문구 (docs/작업 단위 타임아웃과 마무리 설계.md 참조) ----------
//
// per-run 마무리 문구와는 '별개의 두 가지'다: per-run은 "이번 run의 예산이 다 썼다"이고,
// 작업 타임아웃은 "전체 작업이 종료 시점에 도달해 곧 끝난다"이다. 의미가 종종 반대다
// (특히 planner: per-run은 "멈추지 말고 계획을 계속하라"고 하지만, 작업 타임아웃은
// "시점이 됐으니 계획을 멈추고 마지막 판정을 하라"고 한다). worker/planner에만 설정한다.

// WrapupTaskTimeoutOverride / …TurnsOverride: 작업 타임아웃 마무리 문구와 턴 수의 DB 오버라이드
// (agents.task_timeout_wrapup_prompt / _max_turns에 연결되며, worker/planner에만 적용).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**전체 작업이 타임아웃 상한에 도달해 곧 종료됩니다**(이번 run의 예산이 아니라 전체 탐색이 종료 시점에 도달한 것입니다). 이것이 마지막 기회입니다: (1) 이미 식별했지만 아직 기록하지 않은 내용을 '전부' DB에 반영하십시오 — 신규 자산은 insert_assets, 탐색 결론/사실은 record_fact, 확인된 취약점은 report_finding으로; (2) 더 이상 새로운 명령/탐색을 시작하지 마십시오; (3) **마지막으로 순수 텍스트 한 문장으로만** 이 의도에서 얻은 핵심 결론을 요약해 주십시오."

const plannerTaskTimeoutDefault = "**전체 작업이 타임아웃 상한에 도달해 곧 종료됩니다**(이번 라운드가 아니라 전체 작업이 종료되는 것입니다). 현재까지의 '모든' 사실과 발견을 바탕으로 마지막 목표 판정을 해 주십시오: 증거로 이미 달성이 증명된 목표는 prove_goal을 호출해 met으로 표시하십시오(빠뜨리지 마십시오). **더 이상 새로운 의도를 생성하지 마십시오**(이 시점에 의도를 파견해도 더 이상 실행되지 않습니다). 판정이 끝나면 바로 마무리하면 되고, 요약 텍스트를 출력할 필요는 없습니다."

// TaskTimeoutWrapupDefault는 특정 agent의 작업 타임아웃 내장 기본 마무리 문구를 반환한다
// (백오피스의 자리표시자/기본값 복원용).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 설정되지 않은 경우(mainagent/chat)는 빈 문자열을 반환
}

// resolveTaskTimeoutWrapup: DB 오버라이드(비어있지 않음) > 내장 기본값. 빈 문자열은 해당
// agent에 작업 타임아웃 문구가 없음을 의미하며(worker/planner가 아닌 경우), 이때 호출자는
// per-run 문구로 되돌려야 한다.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // 기본적으로 per-run 턴 수를 그대로 사용
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → 이번 run이 작업 deadline에 의해 압박받는 경우: Timeout으로 마무리된
//     경우=작업 종료 시점 도달→작업 타임아웃 문구; MaxTurns로 마무리된 경우=압박 구간 내
//     스텝이 먼저 소진되고 작업 시간이 몇 분 남은 경우→per-run 문구로 되돌림.
//   - clamped=false → 작업이 아직 여유 있는 경우: 두 reason 모두 per-run 문구를 사용한다
//     (즉 wrapupSettlement로 퇴화).
//
// harness에 넘기는 PromptByReason은 마무리 시점에 '실제' reason에 따라 그 자리에서
// 선택하므로, build 시점의 불일치가 없다.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // 기본값(clamped가 아닐 때 두 reason 모두의 값이기도 함)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // 작업 종료 시점 도달
				harness.ReasonMaxTurns: perRun, // 스텝이 먼저 소진되고 작업 시간은 아직 남음
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
