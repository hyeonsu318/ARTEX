package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**취약점 번호 규약**: finding_id는 독립된 취약점 기록 ID이고, finding_node_id는 탐색 노드 ID입니다. list_findings / list_task_findings / node_detail / get_task_node_detail의 id는 탐색 노드 ID로 유지되므로, 독립 번호는 같은 응답에 들어 있는 finding_id에서 읽어야 합니다. get_finding_traffic / bind_finding_traffic은 독립 finding_id를 사용합니다. 기존 update_finding_report의 finding_id 파라미터에는 여전히 finding_node_id를 전달합니다. report_finding 첫 줄의 숫자를 증거 도구에 사용하지 말고, 번호 오류가 나더라도 다른 숫자를 추측해서 넣지 마세요."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\n기본적으로 보고서 Agent가 보고서를 작성하기 전에 트래픽을 대조하여 바인딩합니다. 보고자는 evidence에 검증 명령, 핵심 출력, 이미 확보한 실제 트래픽 ID와 그 용도를 남겨 두어, 보고서 Agent가 실행 기록과 대조해 확인할 수 있게 하세요. 바인딩을 위해 패킷을 따로 조회할 필요는 없습니다. 명시적 즉시 바인딩도 호환됩니다: traffic_refs 또는 evidence_hint_id로 검증된 참조를 제출할 수 있으며, 후자는 이 작업에 지정된 hint의 구조화된 참조를 읽어 옵니다. 둘 중 하나라도 유효하지 않으면 이번 보고 전체가 실패합니다. TCP/패킷 없음의 경우 이러한 선택 파라미터는 필요하지 않습니다. 반환되는 finding_id와 finding_node_id는 각각 독립 기록과 탐색 노드를 나타냅니다."
		case "add_hint", "add_task_hint":
			note = "\n확인된 취약점을 인계할 때는 해당 힌트의 traffic_refs에 검증된 트래픽의 ID, 용도, 설명, 순서를 유지하고(단건은 최상위에, 일괄은 해당 hints 요소에 넣음), text에는 그 트래픽이 입증하는 구체적인 취약점을 설명하세요. 호출자는 텍스트만 인계하고 기존 트래픽 참조를 버려서는 안 됩니다. 검증되지 않은 후보는 증거로 전달할 수 없습니다."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**트래픽 증거 인계(선택)**: 자동 바인딩은 기본적으로 취약점이 저장된 뒤 보고서를 작성하기 전에 보고서 Agent가 수행합니다. 보고자는 evidence에 검증 명령, 핵심 출력, 이미 확보한 실제 트래픽 ID와 그 용도를 남기고, 작업에서는 intent_id를 함께 전달하여 보고서 Agent가 추적할 수 있게 하세요. 바인딩을 위해 패킷을 따로 조회할 필요는 없습니다. Auto / Planner가 대신 보고할 때는 실행자가 이미 가진 참조를 버리지 마세요. add_hint / add_task_hint는 traffic_refs로 인계할 수 있으며, 명시적 즉시 바인딩은 report_finding의 traffic_refs / evidence_hint_id와 여전히 호환됩니다. TCP이거나 패킷이 없는 경우에는 그대로 등록하고, ID를 추측해서는 안 되며 패킷을 보충하려는 목적만으로 재탐색해서도 안 됩니다."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\n플랫폼 대화에 작업 컨텍스트가 없을 때는 report_finding을 직접 호출하지 말고, add_task_hint로 해당하는 기존 작업에 인계하여 작업 Agent가 등록하도록 한 뒤 list_task_findings로 결과를 확인하세요."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\n목표 완료를 판정하기 전에, 이번에 이미 확보한 증거의 보고/인계를 먼저 마치세요. 증거 인계가 끝나지 않았는데 텍스트 취약점이 등록되었다는 이유만으로 작업을 종료하거나 Worker를 취소하지 마세요. 패킷이 없는 경우에는 기다리거나 억지로 캡처할 필요가 없습니다."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**보고 전 트래픽 자동 연결(활성화됨)**: 당신은 이번에 트리거된 취약점에 대해 트래픽을 대조하여 바인딩한 뒤 보고서를 작성할 책임이 있습니다. 먼저 report_finding이 반환한 JSON 또는 get_task_node_detail / list_task_findings에서 명확한 finding_id와 finding_node_id를 확보하세요. 취약점 상세, 해당 의도의 실행 기록 및 기존 증거 목록을 읽고, 보고자가 인계한 실제 ID를 우선 사용하세요. 이번 검증이 HTTP이고 트래픽 도구를 사용할 수 있다면, traffic_search로 후보를 추린 뒤 traffic_get으로 요청/응답이 실제로 해당 취약점을 뒷받침하는지 하나씩 확인하세요. 도메인과 시간은 후보를 추리는 데에만 쓰이며 귀속을 증명하지 않습니다. 확인된 증거는 재현 순서대로 bind_finding_traffic(finding_id, traffic_refs)로 연결하고, baseline / proof / verification / supporting 중에서 선택하여 용도를 설명하세요. 이번 취약점에 대해서만 작업할 수 있으며, 취약점을 중복 생성하거나 대상을 다시 탐색하지 마세요. 바인딩이 성공하면 get_finding_traffic을 다시 호출해 최신 version을 얻고 필요한 본문을 읽은 뒤, 실제로 읽은 version을 evidence_version으로 update_finding_report에 전달하세요(그 finding_id 파라미터에는 여전히 finding_node_id를 사용). 이미 바인딩된 것은 중복으로 추가할 필요가 없습니다. TCP이거나, 수집되지 않았거나, 도구를 사용할 수 없거나, 정확히 일치하는 것이 없으면 자동 바인딩을 건너뛰고 텍스트/명령 증거에 근거해 정상적으로 보고서를 작성하며 그 사유를 설명하세요. 트래픽 개수를 채우려고 추측해서는 안 됩니다. 바인딩에 실패했다면 성공했다고 주장하지 말고, 기존 증거를 보존한 채 보고서에 바인딩되지 않은 사유를 설명하세요."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "선택: 검증이 끝났고 이 힌트의 구체적인 취약점에 대응되는 트래픽 참조로, 순서를 유지함; 인계 후 report_finding에 evidence_hint_id를 전달해 이 참조들을 함께 가져갈 수 있음.", "items": obj(map[string]any{"traffic_id": str("실제 트래픽 ID"), "role": str("baseline / proof / verification / supporting"), "note": str("해당 트래픽이 뒷받침하는 결론")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d 는 이 작업의 힌트 노드여야 합니다(상속된 힌트는 바인딩에 직접 사용할 수 없음)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
