package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa는 norma v0.4.0에서 도입된 "모델 주도 컨텍스트 압축" 메커니즘으로, 플랫폼의
// 실험 기능으로서 사용자가 시스템 설정에서 켜고 끌 수 있다. 내장 compaction과는
// 상호 배타적이다: noaadapter.Enable이 유일한 진입점이며, 한 번에 컨텍스트 관리자
// (Compactor), Compress 도구, 그리고 3단 상주 프롬프트를 연결한다. Enable을 호출하지
// 않으면 꺼진 상태다(내장 compaction이 평소대로 동작). 켜고 끄는 여부는 각 agent가
// 주입하는 noaEnabledFn이 해석하며, run마다 한 번씩 읽으므로 전환은 이후에 시작되는
// run에만 영향을 주고 agent를 재생성할 필요는 없다.

// enableNoa는 리졸버가 켜짐을 보고했을 때 noa를 opts에 연결한다. archiveRoot는
// 압축 원문을 영속화하는 기준 디렉터리이며(전역 workDir를 사용해 각 agent가
// <workDir>/noa 아래에 통일해서 두고, 작업/의도 디렉터리별로 흩어지지 않는다),
// sessionID는 그 아래의 아카이브 하위 디렉터리를 명명한다(전역적으로 유일하므로
// 같은 기준 디렉터리 내에서 충돌하지 않는다).
//
// noa는 실험 기능이다: 연결에 실패해도 실제 작업을 중단시켜서는 안 된다. 오류 발생
// 시 onWarn으로 보고하고 내장 압축으로 되돌린다.
// 활성화에 성공하면 opts.Compaction을 비워, agentcore가 "두 개의 컨텍스트 관리자가
// 동시에 설정됨"으로 경고하는 것을 막는다.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa 압축 활성화 실패, 내장 압축으로 되돌림: " + err.Error())
		}
		return
	}
	// Compactor가 Compaction을 대체하지만 둘이 공존하면 agentcore가 매번 경고하므로 명시적으로 비운다.
	opts.Compaction = nil
}
