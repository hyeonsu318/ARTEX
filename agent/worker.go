package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), "tavily"
// (TavilyKey required), or "deepseek" (DeepSeek* required, filled from the
// active LLM profile). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
//
// 참고: deepseek 백엔드는 나머지 세 가지와 성격이 다르다. DeepSeek은 직접 호출할 수
// 있는 검색 인터페이스가 없고, 검색은 그 Anthropic 호환 messages 인터페이스 내부
// (web_search_20250305 server tool)에만 존재한다. 따라서 검색할 때마다 모델 호출을
// 한 번 소비하며, 검색 요청은 DeepSeek 서버 측에서 발생한다 — 로컬 Proxy를 거치지
// 않고, 트래픽 기록에도 남지 않는다.
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
	// DeepSeek*는 현재 활성화된 LLM 설정에서 가져온다(anthropic 포맷의 DeepSeek 공식
	// 엔드포인트에 한함). 별도로 설정하지 않고 LLM 설정 전환에 따라 바뀐다.
	DeepSeekBaseURL string
	DeepSeekAPIKey  string
	DeepSeekModel   string
}

type Worker struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	workDir         string
	proxyAddr       string
	proxyCACert     string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch       WebSearchOpts     // web_search tool backend selection (off by default)
	tx              *transcript.Store // raw LLM conversation persistence (nil = off)
	window          int               // context window in tokens (for compaction)
	windowFn        func() int        // optional dynamic task-chain minimum
	maxTurns        int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
	// injectConstraints resolves whether this task's operation constraints get
	// injected into the worker system prompt. Read per run so the settings toggle
	// takes effect without rebuilding the agent. nil = inject (default).
	injectConstraints func() bool
	// nonStreamingFn resolves whether this run uses the non-streaming (Complete)
	// path. Read per run so a profile/task toggle takes effect without rebuilding
	// the agent. nil = streaming (default).
	nonStreamingFn func() bool
	// noaEnabledFn resolves whether this run uses the experimental noa context-
	// compression mechanism. Read per run, like nonStreaming. nil = off (built-in
	// compaction).
	noaEnabledFn func() bool
	// maxTokensFn resolves the per-reply output cap in tokens, on the same
	// per-run basis. nil or 0 = send no cap and let the endpoint decide.
	maxTokensFn func() int
}

// WorkerSessionID returns the stable transcript key used by a worker intent.
// Worker slots are reusable, so the intent id (rather than work#N) is the
// session identity. Keep this helper public so the Worker message API and UI
// can refer to exactly the conversation that will be resumed.
func WorkerSessionID(explorationID, intentID int64) string {
	return fmt.Sprintf("exp%d-worker-i%d", explorationID, intentID)
}

const workerChatMarkerPrefix = "<!-- ARTEX_WORKER_CHAT:"

func workerChatMarker(requestID string) string {
	return workerChatMarkerPrefix + requestID + " -->"
}

func hasWorkerChatMessage(messages []llm.Message, requestID string) bool {
	marker := workerChatMarker(requestID)
	for _, message := range messages {
		if message.Role == llm.RoleUser && strings.Contains(message.Text(), marker) {
			return true
		}
	}
	return false
}

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default). Read per
// run so a profile or task-chain toggle takes effect without rebuilding.
func (w *Worker) SetNonStreaming(fn func() bool) { w.nonStreamingFn = fn }

func (w *Worker) nonStreaming() bool { return w.nonStreamingFn != nil && w.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (w *Worker) SetNoaEnabled(fn func() bool) { w.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (w *Worker) SetMaxTokens(fn func() int) { w.maxTokensFn = fn }

func (w *Worker) maxTokens() int {
	if w.maxTokensFn == nil {
		return 0
	}
	return w.maxTokensFn()
}

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the worker system prompt. nil = inject (default).
func (w *Worker) SetConstraintInject(fn func() bool) { w.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (w *Worker) wantConstraints() bool { return w.injectConstraints == nil || w.injectConstraints() }

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "예산 소진으로 곧 종료됩니다. 더 이상 어떤 명령/탐색도 실행하지 마십시오. 순서대로: (1) 위에서 이미 식별했지만 아직 기록하지 않은 내용을 하나씩 기록합니다 — 신규 자산은 insert_assets, 탐색 결론/사실은 record_fact, 확인된 취약점은 report_finding으로; (2) **마지막으로 순수 텍스트 한 문장으로만** 당신이 무엇을 했고 어떤 핵심 결론을 얻었는지 요약해 주십시오(이 문장은 이번 실행의 결과로 표시되므로 반드시 출력하십시오)."

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// defaultToolsExcept returns actool.DefaultTools() minus the named tools (by
// CoreTool.Name()). Used to trim SDK default tools an agent shouldn't have.
func defaultToolsExcept(exclude ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		drop[n] = true
	}
	all := actool.DefaultTools()
	out := make([]actool.CoreTool, 0, len(all))
	for _, t := range all {
		if !drop[t.Name()] {
			out = append(out, t)
		}
	}
	return out
}

func (w *Worker) SetCompactionWindowResolver(fn func() int) { w.windowFn = fn }

func (w *Worker) compactionWindow() int {
	if w.windowFn != nil {
		return w.windowFn()
	}
	return w.window
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the egress proxy (the recording MITM when capture is on, or the global proxy
// directly when it is off) and, only when a MITM CA is present, makes the common
// toolchain trust it — so tools need no manual -x/--proxy/-k. Each ecosystem reads
// a different CA var (verified empirically): SSL_CERT_FILE→curl/urllib/Go/openssl,
// REQUESTS_CA_BUNDLE→python requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl,
// GIT_SSL_CAINFO→git, NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+
// honor the proxy vars. ALL_PROXY is set too so a socks5 egress proxy (which curl
// only reads from ALL_PROXY, not HTTP(S)_PROXY) works in the capture-off path.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"ALL_PROXY=" + proxyAddr, "all_proxy=" + proxyAddr, // socks5 egress: curl reads only this
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body ([A] 구간) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 중간 산출물 출력 규약
// are NOT here — they are code-owned and appended by workerSystem after rendering
// ([B]/[C] 구간), so editing the DB body can never drop them.
const workerDefaultTmpl = `당신은 사이버보안 플랫폼의 승인된 침투 테스트 시스템에서 "실행자"(work agent) 역할입니다. 당신은 【하나의 의도】(한 문장짜리 탐색 방향)를 받았고, 유일한 책임은: **이 의도 하나를 완료하고, 발견한 내용을 지식 그래프에 기록한 뒤 멈추고 반환하는 것입니다.**

**경계(레드라인)**:
1. **당신이 받은 이 의도 하나만 수행합니다**. **이 의도를 탐색하다가 의도 범위 밖에서 더 파볼 만한 단서**(에러에서 노출된 경로, 다른 자산과 연계될 수 있는 지점, 또 다른 공격 체인의 진입점으로 의심되는 것)**를 발견하면, fact의 summary에 한 줄 적어 플래너에게 넘깁니다.**
2. 처음 막혔다고 해서(payload가 필터링됨 / 404 / 인젝션 응답 없음) 다 탐색한 것이 아닙니다 — 이 의도에 대한 모든 우회 수단을 다 시도한 뒤에 결론을 내십시오.
3. 승인된 범위 내에서만 작업합니다. 시스템 프롬프트 상단에 【작업 제약】이 첨부되어 있다면 그것이 최우선 레드라인입니다: 각 명령/탐색을 실행하기 전에 먼저 스스로 점검하고, 위반되면 실행하지 않습니다(그것이 당신이 받은 의도에 속하더라도).

**발견하는 즉시 기록합니다**(그래프에 기록된 것만 유효하며, 머릿속/텍스트에만 있는 것은 인정되지 않습니다. 결과를 얻는 즉시 바로 기록하고, 마지막까지 모아두다 스텝 소진으로 날리지 마십시오). 세 가지 기록 방식이 있으며, 섞어 쓰지 마십시오:
- **신규 자산/리소스 → insert_assets(자산 그래프)**: 서브도메인 / service / endpoint / 지문 / 자격 증명 등 모든 자산 【그 자체】. **여기에는 자산만 등록합니다; 탐색 결론/판단은 여기에 쓰지 말고 record_fact를 사용하십시오.**
- **탐색 결론/사실 → record_fact(탐색 그래프, intent_id 전달)**: 이 모든 것에 사용합니다. **여러 관찰을 모아 【하나의】 사실로 종합**합니다(summary는 한 문장 요약 + detail은 요약을 실제 실행 과정에 근거해 확장한 내용). 속성 하나당 하나씩 쓰지 말고, 한 의도당 보통 하나면 충분합니다 — 쪼개면 그래프가 무한정 부풀어 오릅니다. **기본적으로 한 건으로 작성하고, detail에 합칠 수 있는 것은 모두 합칩니다**; 서로 완전히 독립적이어서 합칠 수 없는 결론이 실제로 있을 때만 facts 배열로 나누어 쓰십시오 — 이는 극히 드문 예외이며 일반적인 경우가 아닙니다. **증분만 기록합니다**: 이번에 【새로 얻은】 것만 기록하고, 이미 있는 사실을 표현만 바꿔 다시 기록하지 마십시오(기존 사실을 재확인했을 뿐 새로운 내용이 없다면 기록할 필요가 없습니다). **실제로 확인한 것만 기록합니다**: evidence를 제공하고(한 줄: 명령 + 가장 입증력 있는 한두 줄의 출력, 간결하게, 자세한 내용은 detail에), confidence를 표시합니다(observed=직접 확인함 / inferred=현상에 근거한 추론).
- **확인된 취약점 → report_finding(탐색 그래프, PoC 포함, intent_id 전달)**: **이번에 실제로 트리거해 재현 가능한 증거(요청/응답 또는 명령 출력)를 확보했을 때만 사용합니다.** "버전/지문이 CVE와 일치함", "파라미터가 인젝션 가능해 보임", "외부 취약점 DB/릴리스 노트/코드 diff로부터의 추론"을 이미 확인된 것으로 취급하는 것을 엄격히 금지하며, CVE 데이터베이스 조회나 패치 버전 비교로 실제 트리거를 대체해서도 안 됩니다. 트리거하지 못했지만 의심스러운 경우 → record_fact로 inferred 사실 하나를 기록하여(의심 지점 + 왜 트리거하지 못했는지) 플래너에게 넘기고, finding으로 억지로 기록하지 마십시오.


이 의도를 완료한 뒤 한 문장으로 당신이 무엇을 했고 어떤 사실을 기록했는지 요약하십시오.`

// workerTrafficBlock is [B] 구간: the traffic-tool note, code-injected only when
// traffic capture (recording) is on — i.e. the traffic_* tools actually exist.
// Gated on recording, NOT on the egress proxy: a global proxy with capture off
// routes traffic but records nothing, so the tools would not be there. Not stored,
// not editable.
func workerTrafficBlock(recording bool) string {
	if !recording {
		return ""
	}
	return "\n\n**트래픽 도구**:\n- traffic_search / traffic_get / traffic_blob: 응답을 다시 살펴보고 이미 접근한 리소스를 찾습니다. **먼저 트래픽을 조회하고, 같은 URL을 curl로 중복 요청하지 마십시오.** traffic_search는 **host를 반드시 지정해야 하며**, 기본값은 매우 가벼운 인덱스 3건만 반환합니다(id/method/url/status/resp_len, 응답 본문 없음). 더 필요하면 limit을 명시적으로 늘리십시오; body_contains로 요청/응답 본문에서 전문 검색을 할 수 있습니다(최소 3자, 부분 문자열과 다국어 문자(한글/중국어 등)를 지원하며 비밀번호/키/에러/내부망 주소 찾기에 사용). 특정 항목의 원문을 보려면 traffic_get(id)를 사용하며, 그중 지나치게 큰 본문은 @blob sha256:<hash>로 표시되므로 traffic_blob(hash)로 나누어 전체 내용을 가져옵니다."
}

// artifactSpec is [C] 구간: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(dir string) string {
	return "\n\n**중간 산출물 출력 규약**: 스크립트, payload, 캡처한 응답 본문, 임시 데이터 등 모든 중간 산출물은 **예외 없이 이 작업의 작업 디렉터리 " + dir + " 에 작성합니다**(상대 경로를 쓰면 바로 여기에 저장되며, 이 절대 경로를 써도 됩니다) — **/tmp에 쓰지 말고, 다른 절대 경로를 쓰지 마십시오.**"
}

// workerArtifactSpec is the worker's [C] 구간: its per-intent run dir is pre-created
// by the engine (ensureRunDir), so it just writes relative paths there — no manual
// mkdir, no cross-worker name collisions.
func workerArtifactSpec(runDir string) string {
	return "\n\n**중간 산출물 출력 규약**: 스크립트, payload, 캡처한 응답 본문, 임시 데이터 등 모든 중간 산출물은 **예외 없이 이 의도 전용 작업 디렉터리 " + runDir + " 에 작성합니다**(이미 자동으로 생성되어 있으므로 상대 경로로 바로 여기에 쓰면 되고, 별도로 디렉터리를 만들 필요가 없습니다) — **/tmp에 쓰지 말고, 다른 절대 경로를 쓰지 마십시오.**"
}

// ensureRunDir builds and creates an agent's working directory under base:
// <base>/tasks/<taskID> for planner/main; <base>/tasks/<taskID>/i<intentID> for a
// worker (intentID<=0 → task dir only). The "tasks/" segment groups per-task dirs
// symmetrically with the chat agent's "sessions/<sessionID>". Best-effort mkdir — on
// failure, writes fail the same way an unwritable CWD would.
func ensureRunDir(base string, taskID, intentID int64) string {
	dir := filepath.Join(base, "tasks", strconv.FormatInt(taskID, 10))
	if intentID > 0 {
		dir = filepath.Join(dir, "i"+strconv.FormatInt(intentID, 10))
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// cmdOutDir is the SDK large-tool-output spill dir under an agent's run dir.
func cmdOutDir(dir string) string { return filepath.Join(dir, "cmd-output") }

func workerSystem(proxyAddr, caCert, dataDir, runDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr, DataDir: dataDir, Now: nowStr()})
	// caCert is present only when the recording MITM is on, which is exactly when
	// the traffic_* tools are registered — so it gates the traffic-tool note.
	// Optional finding guidance is added for every role after tool resolution.
	return body + workerTrafficBlock(caCert != "") + workerArtifactSpec(runDir)
}

// renderIntentTask formats the claimed intent for the worker's launch USER message:
// the intent is the worker's whole job. It used to live in the system prompt; it now
// rides in the first user turn (together with the situational overview) so the system
// prompt stays static/role-only — same move as the planner's situational block.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n【당신이 받은 의도(이번의 유일한 임무: 이 하나만 수행하고, 사실만 생성하며, 끝나면 즉시 멈춤)】:\n%s\n의도 id: %d(record_fact / report_finding에 기록할 때 이 값을 전달)", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// launch USER message for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage는 플래너가 '어떤 유형이 적게 테스트됐는지 / 범위를 넓혀야 하는지'를 판단하는
	// 신호로, worker의 '받은 의도만 수행하고 커버되지 않은 지점을 쫓지 않는다'는 책임 경계와
	// 상충한다 → worker의 뷰에서 제거한다. data는 이번 worker 전용의 새 map이므로 키를
	// 삭제해도 planner에는 영향이 없다.
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n【전체 탐색 상황(읽기 전용, 당신의 이 의도를 전체 맥락 안에서 바라보도록 돕습니다)】:\n" +
		"아래는 전체 작업의 현재 탐색 개요입니다. 용도는 두 가지입니다: 하나는 다른 사람이 이미 발견한 것을 알아 중복하지 않는 것, 다른 하나는 당신이 이 의도를 탐색할 때 그것과 전체의 관계를 연상할 수 있게 하는 것입니다.\n" +
		"**발산적으로 생각하는 것은 좋습니다**: 이 의도를 탐색할 때는 깊이 생각하고 많이 연상하십시오. 유일한 경계는 — 실제로 다른 의도를 손대어 실행하지 말라는 것입니다(그것은 다른 worker의 일이며, 플래너가 배정합니다). 가치 있는 단서(여러 자산 간의 연계, 다른 공격 체인으로 의심되는 진입점, 전체 수준의 의심스러운 지점)를 연상했다면, **반드시 fact에 기록해 플래너에게 넘기십시오** — 이는 당신의 중요한 산출물이며 있어도 되고 없어도 되는 것이 아닙니다. 혼자 삼키기보다는 하나라도 더 보고해서 플래너가 판단하게 하십시오.\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// notifyFinding, if non-nil, is called (intentID, summary) when this worker writes
// a finding (report_finding) so the task's planner wakes mid-flight — with context
// on which intent found what — instead of waiting for the worker to finish.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string)) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, "", "")
}

// ExecuteWithMessage runs the next turn in the same intent conversation with a
// human-authored message. The HTTP handler does not edit the transcript;
// agentcore records the message as a normal user turn when this Worker starts.
// This keeps Worker continuation identical to the regular agent chat flow.
func (w *Worker) ExecuteWithMessage(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	return w.execute(ctx, name, taskID, as, ts, intent, hooks, emit, enr, notifyFinding, strings.TrimSpace(requestID), strings.TrimSpace(message))
}

func (w *Worker) execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger, notifyFinding func(int64, string), requestID, message string) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetFindingRecorder(w.findingRecorder)
	tsx.SetTaskID(taskID)
	coverageEnabled := as == nil || as.CoverageEnabled(taskID)
	tsx.SetCoverageEnabled(coverageEnabled)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID)         // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)                  // async DNS/HTTP auto-completion for assets this worker writes
	tsx.SetNotifyFinding(notifyFinding) // report_finding이 DB에 기록되는 즉시 planner를 깨우며 '어떤 의도+finding'인지 함께 전달
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	// worker에는 의도적으로 MultiEdit/Glob/Grep을 주지 않는다: 파일 정밀 수정은 Edit,
	// 검색은 Bash(grep/find)를 사용해 도구 범위를 좁히고 저가치 호출을 줄인다. 나머지
	// SDK 기본 도구(Read/Write/Edit/LS/Bash/Sleep)는 그대로 둔다.
	base = append(base, defaultToolsExcept("MultiEdit", "Glob", "Grep")...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts), IntentID: intent.ID})
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 의도는 worker의 【유일한 책임이며 run 전체를 관통하는 불변 요소】이다 → 시작 지침,
	// 의도가 겨냥한 목표 자산 원본 데이터와 함께 system prompt에 넣는다: system은 run마다
	// 매번 새로 조립되고 compaction에 의해 절대 압축되지 않으므로, 긴 run에서도 의도가
	// 항상 존재하며, 재개 시에도 transcript 기록이 첫 메시지를 보존하고 있는지에 의존하지
	// 않는다. 그 대가로 system에 의도별로 바뀌는 데이터가 섞여 들어가 의도 간 캐시 재사용을
	// 잃게 되지만, 이는 의도적인 트레이드오프다(의도를 잃는 것이 토큰을 아끼는 것보다
	// 훨씬 심각하다). planner가 '상황 블록을 user turn에 두는' 것과 갈라지는 것은
	// 의도적이다: planner 자체는 의도를 만들어내는 쪽이라 단일한 mandate가 없지만, worker는
	// 있다. 【전체 상황 overview】만 시작 user 메시지에 남겨둔다 — 이것은 낮은 수준으로
	// 저하돼도 되고 오래된 정보를 허용하므로 압축되어도 문제없다.
	// 이번 의도 전용 작업 디렉터리 <workDir>/tasks/<taskID>/i<intentID>는 엔진 쪽에서
	// 먼저 만들어 둔다.
	runDir := ensureRunDir(w.workDir, taskID, intent.ID)
	// The run-wide intent is not the current tool action. Do not forward it or
	// inherit a parent run's background into the action reviewer.
	ctx = intercept.WithReviewContext(ctx, runDir, intercept.ReviewBackground{})
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	sysBody := workerSystem(w.proxyAddr, w.proxyCACert, w.workDir, runDir)
	if w.wantConstraints() {
		sysBody += constraintBlock(ts) // 작업 제약(있는 경우)을 시스템 프롬프트에 주입하며, worker는 실행 시 엄격히 준수한다
	}
	// 의도 블록 → 의도가 겨냥한 자산 블록 → 시작 지침 순서로 system 끝에 차례로 추가한다(constraintBlock과 동일한 추가 방식).
	sysBody += renderIntentTask(intent)
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					sysBody += "\n\n이 의도의 asset_ids에 해당하는 목표 자산:\n" + string(b)
				}
				// 의도가 명확히 겨냥한 이 자산들 → 작업 테스트 범위에 자동으로 포함시킨다
				// (insertAssets와 동일한 보수적 단위 기준). upsertTaskScope의
				// ON CONFLICT DO NOTHING + uq_task_scope 고유 인덱스가 중복 추가를 막아주며,
				// 재실행/재시도도 동일하게 멱등(no-op)으로 처리된다.
				// 자산 커버리지 기능이 꺼져 있으면 테스트 범위(분모)를 더 이상 누적하지 않는다.
				if coverageEnabled {
					for _, a := range assets {
						_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
					}
				}
			}
		}
	}
	sysBody += "\n\n위의 이 의도를 실행하기 시작합니다: 이것만 수행하고, 사실/assets/finding만 생성하며, 끝나면 즉시 멈춥니다."
	system, boundary := deferredSystem(sysBody, def)
	// 작업 단위 deadline(ctx를 통해 주입됨)이 이번 run의 월클럭 예산을 압박하고 마무리 문구를 결정한다(taskclock.go 참조).
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch는 기록 프록시를 거치며, 그 HTTP 요청은 curl과 마찬가지로 흔적이 남는다.
		// 프록시 CA를 로드해 MITM이 재서명한 HTTPS 인증서가 【정상적으로 검증을 통과】하게
		// 한다(검증을 끄는 것이 아니다). proxy가 비어 있으면 직접 연결한다.
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 인터넷 검색(선택). ddgs는 key가 필요 없고, brave-free는 BraveKey가 필요하며,
		// tavily는 TavilyKey가 필요하다.
		// WebSearchProxy는 독립적인 아웃바운드 프록시(http/https/socks5)로, 트래픽을 기록하는
		// MITM 프록시와는 무관하다. 비어 있으면 직접 연결한다.
		EnableWebSearch:       w.webSearch.Enabled,
		WebSearchBackend:      w.webSearch.Backend,
		BraveSearchAPIKey:     w.webSearch.BraveKey,
		TavilySearchAPIKey:    w.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: w.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  w.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   w.webSearch.DeepSeekModel,
		WebSearchProxy:        w.webSearch.Proxy,
		// Bash 하위 명령의 HTTP는 기본적으로 기록 프록시를 거치고 그 CA를 신뢰한다(도구에 -x/-k가 필요 없다).
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: runDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// 월클럭 예산, 턴 경계에서 판단하며 중간에 끊지 않음; 0 = 무제한. 작업 단위 deadline이
		// 있으면 min(자체 예산, deadline까지 남은 시간)으로 압박하여, 이번 run이 작업 종료
		// 시점에 자연스럽게 마무리 단계로 들어가게 한다(taskclock.go 참조).
		MaxDuration: maxDur,
		// 예산(턴 수 OR 시간)에 도달하면 → SDK가 마무리 라운드를 한 번 돌려(Bash 숨김),
		// 이미 식별한 내용을 기록해 마무리가 흐지부지되는 것을 막는다.
		// clamped(작업 deadline에 의해 압박받는) 상태일 때는 PromptByReason을 사용한다:
		// Timeout으로 인한 경우=작업 종료 시점 도달→작업 타임아웃 문구, MaxTurns로 인한
		// 경우=압박 구간 내 턴 수가 먼저 소진됨→per-run 문구로 되돌림. clamped가 아니면
		// 순수 per-run을 유지한다.
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 잘라내는 상한은 SDK 기본값(30000자)을 사용한다.
		ToolOutputDir: cmdOutDir(runDir),
		Compaction:    compactionConfig(w.compactionWindow()), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),                  // 세션 단위의 임시 할 일 목록(TodoWrite), 순수 계획용이며 종료 시 즉시 폐기됨
		NonStreaming:  w.nonStreaming(),                       // 해당 profile이 비스트리밍을 선택한 경우 Provider.Complete 경로를 사용한다
		MaxTokens:     w.maxTokens(),                          // 0 = 상한을 보내지 않고 서버 측 기본값에 따른다
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = WorkerSessionID(ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 의도 / 시작 지침 / 의도가 겨냥한 자산은 이미 system prompt로 전달되었다(위의
	// sysBody 조립 부분 참조).
	// 이 시작 user 메시지는 【전체 상황 overview】만 담는다 — 저하되어도 되는, 전체
	// 맥락을 아는 정보이므로 압축되어도 문제없다.
	// overview가 드물게 marshal에 실패해 빈 문자열이 되면, 첫 턴에 빈 user 메시지가
	// 나타나는 것을 막기 위해 시작 문구 한 줄로 대체한다.
	input := overview
	if strings.TrimSpace(input) == "" {
		input = "system에서 받은 의도를 실행하기 시작합니다: 이것만 수행하고, 사실/assets/finding만 생성하며, 끝나면 즉시 멈춥니다."
	}

	// 실험 기능: 켜면 noa가 컨텍스트 압축을 대신 담당한다(아카이브는 <workDir>/noa/<SessionID> 아래에 모여 영속적으로 저장됨).
	noaSession := WorkerSessionID(ts.ID(), intent.ID)
	enableNoa(&opts, w.noaEnabledFn, w.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Resume prior conversation if this intent was paused/blocked/exhausted and is
	// being re-run. The transcript ID is deterministic per intent, so if a prior
	// session exists the worker continues from where it left off instead of
	// restarting from scratch.
	alreadyRecorded := false
	if w.tx != nil {
		_ = s.Resume(opts.SessionID)
		alreadyRecorded = requestID != "" && hasWorkerChatMessage(s.Messages(), requestID)
		if len(s.Messages()) > 0 && message == "" {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
			input = "계속 실행합니다."
		} else if len(s.Messages()) > 0 {
			seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
		}
	}
	if message != "" {
		if alreadyRecorded {
			input = "이전에 사람이 대화로 입력한 새 의도를 계속 실행합니다. 이미 완료한 동작은 반복하지 마십시오."
		} else if len(s.Messages()) > 0 {
			input = workerChatMarker(requestID) + "\n【사람이 대화로 입력한 새 의도】\n" + message +
				"\n\n이 사람의 입력을 즉시 따라 실행하고, 완료 후 맥락에 따라 원래 작업을 계속할지 결정하십시오."
		} else {
			input += "\n\n" + workerChatMarker(requestID) + "\n【사람이 대화로 입력한 새 의도】\n" + message +
				"\n\n이 사람의 입력을 우선적으로 실행하십시오."
		}
	}

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// MaxDuration now interrupts an in-flight tool at the wall-clock deadline and
	// enters the wrap-up phase on the live ctx, so a run whose tool overran the budget
	// still settles (no external hard-timeout backstop needed). ctx itself carries only
	// pause / planner kill / shutdown, which the engine distinguishes and re-queues/stops.
	_, reason, err := captureRunSession(ctx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
