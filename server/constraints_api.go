package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 개요 「제약 관리」의 수동 CRUD 인터페이스 + 주입 범위 스위치 해석. 작업 제약(allow/deny)은 agent 측의
// set_constraints 도구와 같은 task_constraints 테이블에 기록한다. 여기서는 사람이 UI 에서 직접 추가/수정/삭제한다. 제약은 단지
// 프롬프트 컨텍스트일 뿐이므로 추가/수정/삭제 후 planner 에게 알리지 【않는다】. 다음 라운드 계획 시 DB 를 읽어 자연스럽게 반영된다(제품 결정). 모든 변경 handler
// 는 beginTaskOperation/decInflight 를 거쳐 작업 삭제와의 경쟁을 피한다(목표/의도 CRUD 와 동일).

// 주입 범위 스위치의 settings key. 기본값은 모두 켬(GetBool 두 번째 인자 = true).
const (
	settingConstraintsInjectPlanner = "constraints_inject_planner"
	settingConstraintsInjectWorker  = "constraints_inject_worker"
)

// constraintInjectPlanner / constraintInjectWorker 는 작업 제약을 해당 agent 의
// 시스템 프롬프트에 주입할지 보고한다(기본 켬). resolver 로 planner/worker 에 전달되어 매 라운드 읽으므로 스위치 변경이 즉시 반영된다.
func (s *Server) constraintInjectPlanner() bool {
	return s.m.pg.GetBool(settingConstraintsInjectPlanner, true)
}

func (s *Server) constraintInjectWorker() bool {
	return s.m.pg.GetBool(settingConstraintsInjectWorker, true)
}

// listConstraints 는 이 작업의 모든 작업 제약을 반환한다(allow 먼저, deny 나중).
func (s *Server) listConstraints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	rows, err := t.Store.ListConstraints()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"constraints": constraintDTOs(rows)})
}

// addConstraint 는 작업 제약 하나를 수동으로 추가한다(kind=allow|deny). planner 에게 알리지 않는다.
func (s *Server) addConstraint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업이 삭제되는 중이라 제약을 추가할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text string `json:"text"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "제약 내용은 비워 둘 수 없습니다")
		return
	}
	kind := normalizeConstraintKind(body.Kind)
	if kind == "" {
		writeErr(w, 400, "kind 는 allow 또는 deny 여야 합니다")
		return
	}
	id, err := t.Store.AddConstraint(kind, text, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ConstraintDTO{ID: strconv.FormatInt(id, 10), Kind: kind, Text: text, Origin: "human"})
}

// editConstraint 는 제약 하나(kind + text)를 수동으로 수정한다. planner 에게 알리지 않는다.
func (s *Server) editConstraint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업이 삭제되는 중이라 제약을 수정할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		writeErr(w, 400, "bad constraint id")
		return
	}
	var body struct {
		Text string `json:"text"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "제약 내용은 비워 둘 수 없습니다")
		return
	}
	kind := normalizeConstraintKind(body.Kind)
	if kind == "" {
		writeErr(w, 400, "kind 는 allow 또는 deny 여야 합니다")
		return
	}
	if err := t.Store.UpdateConstraint(cid, kind, text); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ConstraintDTO{ID: strconv.FormatInt(cid, 10), Kind: kind, Text: text})
}

// deleteConstraint 는 제약 하나를 수동으로 삭제한다. planner 에게 알리지 않는다.
func (s *Server) deleteConstraint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "작업이 삭제되는 중이라 제약을 삭제할 수 없습니다")
		return
	}
	defer s.engine.decInflight(t.ID)

	cid, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil || cid <= 0 {
		writeErr(w, 400, "bad constraint id")
		return
	}
	if err := t.Store.DeleteConstraint(cid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// normalizeConstraintKind lowercases + validates the kind; "" on invalid.
func normalizeConstraintKind(k string) string {
	k = strings.TrimSpace(strings.ToLower(k))
	if k == "allow" || k == "deny" {
		return k
	}
	return ""
}

// constraintDTOs converts db rows to the frontend shape.
func constraintDTOs(in []db.Constraint) []ConstraintDTO {
	out := make([]ConstraintDTO, 0, len(in))
	for _, c := range in {
		out = append(out, ConstraintDTO{
			ID:     strconv.FormatInt(c.ID, 10),
			Kind:   c.Kind,
			Text:   c.Text,
			Origin: c.Origin,
			TS:     rfc3339(c.CreatedAt),
		})
	}
	return out
}
