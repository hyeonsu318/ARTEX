package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**취약점 트래픽 증거(선택)**: report_finding으로 취약점을 보고할 때, 이미 확인했고 취약점 결론을 뒷받침하는 HTTP 요청/응답이 있다면 traffic_refs로 재현 순서에 따라 실제 ID를 묶어 연결할 수 있다. 도메인과 시간은 후보를 추리는 용도일 뿐이며 연관성을 단정하지 않는다. TCP 등 비 HTTP 취약점이거나, 수집되지 않았거나, 정확히 일치하는 항목이 없으면 생략하거나 []를 전달하고, evidence에는 명령 출력, 로그 등 검증 가능한 다른 증거를 남기며 연결하지 않은 이유를 설명하는 것을 권장한다. ID를 추측하지 말고, 패킷을 보충하기 위해서만 반복 탐측하지도 않는다."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
