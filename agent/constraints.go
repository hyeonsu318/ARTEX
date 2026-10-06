package agent

import (
	"strings"

	"github.com/Autumn-27/artex/db"
)

// constraintBlock renders this task's operation constraints (task_constraints) as a
// high-priority block appended to the planner/worker system prompt. allow/deny are
// grouped; empty string when there are no constraints (or ts is nil). The framing
// deliberately puts these ABOVE the exploration/expansion heuristics so a declared
// boundary wins the tug-of-war against "chase another entry surface".
func constraintBlock(ts *db.ExplorationStore) string {
	if ts == nil {
		return ""
	}
	rows, err := ts.ListConstraints()
	if err != nil || len(rows) == 0 {
		return ""
	}
	var allow, deny []string
	for _, c := range rows {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if c.Kind == "allow" {
			allow = append(allow, "- "+text)
		} else {
			deny = append(deny, "- "+text)
		}
	}
	if len(allow) == 0 && len(deny) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【작업 제약(최우선순위, 아래의 모든 탐색/확장 휴리스틱보다 우선한다. 의도를 하나 생성할 때마다, 동작을 하나 실행하기 전마다 먼저 위반 여부를 자체 점검해야 하며, 위반 시 진행해서는 안 된다)】:")
	if len(allow) > 0 {
		b.WriteString("\n허용되는 작업:\n")
		b.WriteString(strings.Join(allow, "\n"))
	}
	if len(deny) > 0 {
		b.WriteString("\n금지되는 작업:\n")
		b.WriteString(strings.Join(deny, "\n"))
	}
	b.WriteString("\n(제약 밖의 새로운 목표/새 포트/새 호스트를 발견했다고 해서 권한을 얻은 것은 아니다. 위 허용 범위에 속하지 않는 한 out-of-scope 사실로 기록하고 건너뛰며, 그것을 위해 의도를 파생하거나 동작을 실행해서는 안 된다.)")
	return b.String()
}
