# `/btw` 검증 기록

날짜: 2026-09-10. 브랜치: `codex/btw-side-question`. 기준 커밋: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

독립된 PostgreSQL 테스트 DB와 데이터 디렉터리를 사용했다; 실제 모델 자격증명은 독립된 테스트 환경에만 주입했으며, 코드나 이 기록에는 기록하지 않았고 제품 기본 모델도 수정하지 않았다. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

실제 모델 대화, 반환 객체, 엔지니어링 단언, 그리고 Qwen의 검토 원문은 [validation-2026-09-10.json](validation-2026-09-10.json)에 저장되어 있으며, 여기에는 API 자격증명이 없다.

## 엔지니어링 검사

| 범위 | 결과 | 근거 |
| --- | --- | --- |
| 구조화된 메시지, 도구 인자 깊은 복사 | 통과 | `TestCheckpointDeepCopyAndBoundaries` |
| 요약 / 압축 요청이 덮어쓰지 않음, 완전한 응답과 종료 상태 게시, 반쪽 응답 제외 | 통과 | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 실제 모델 풀 구성원 신원 | 통과 | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 도구 짝, 20쌍 재생, 예산 절단과 초과 에러 | 통과 | `TestBuildRequestCompactionToolPairingAndBudget` |
| 메인과 사이드 병행, 양방향 취소 격리 | 통과 | 블로킹 Provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| 도구 실행 없음, 스트리밍 / 비스트리밍, 실패 시 기존 사용량 | 통과 | `TestServiceNoToolsAndUsageOnFailure` |
| 실제 norma ChatAgent + 로컬 Read 도구, 메인 transcript / 활동 격리 | 통과 | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, 스트리밍 및 비스트리밍 하위 케이스 |
| 영속화, 페이지 분할, 멱등, 재시작 시 부분 답변 보존 | 통과 | `TestSideHistoryIdempotencyPagingAndRecovery` |
| 비우기와 늦게 도착한 쓰기의 경쟁, 부모 리소스 삭제, 버전 비교 | 통과 | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker 아카이브와 복구, v1/v2/v3 | 통과 | `TestSideTaskArchiveVersions` |
| 세 가지 부모 인터페이스, 인증, 리소스 소유, Worker 논리 삭제 | 통과 | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 바쁜 메인 세션에서도 사이드 질문 가능, 독립 SSE 재연결 / 끊김, 취소, 비우기 | 통과 | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 부모 세션당 1 / 전역 4 동시성 | 통과 | 두 개의 `TestSideHTTP…` 케이스 |
| 제출 전 스냅샷 저장, 재시작 후 이어서 질문, 오래된 세션은 스냅샷을 위조할 수 없음 | 통과 | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 캐시된 설정이 삭제되거나 모델이 바뀌면 계속 진행 거부 | 통과 | `TestSideRejectsDeletedOrChangedCachedProfile` |
| 아카이브 전에 취소하고 최종 답변과 사용량이 저장되기를 기다림 | 통과 | `TestSideTaskDrainPersistsBeforeArchive` |
| 스트리밍 소비자가 일찍 취소되어도 사용량은 한 번만 기록되고 사이드 질문 귀속 | 통과 | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| 재시작 후 자동 복구된 Worker / deadline 실행 컨텍스트가 계속 새 스냅샷을 게시 | 통과 | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| 관련 패키지 race 검사 | 통과 | 아래 명령 |
| TypeScript와 프로덕션 빌드 | 통과 | `npx tsc --noEmit`, `npm run build` |
| 새로 추가한 프런트엔드 모듈 Biome | 통과 | `biome check`, 새 모듈 3개 |

별도의 폐기 가능한 데이터베이스에 `ARTEX_PG_DSN`을 설정하면 자동화 검사를 재현할 수 있다(운영 DB를 가리키지 말 것):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

전체 Go 회귀는 모두 녹색이 아니다: `server` 패키지에서 기존의 테스트 두 개가 임시 디렉터리 정리 단계에서 실패하며, 둘 다 `TempDir RemoveAll … directory not empty`를 보고한다:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

수정하지 않은 위 기준 커밋에서 소스를 내보내 같은 격리 환경에서 `server` 패키지를 다시 돌려도 이 두 정리 실패가 재현된다. 기준 실행에서는 `TestCoreTaskLifecyclePG`의 대상 노드 수 단언 실패도 나타났으며, 최종 수정 후의 `server` 회귀에서는 이 단언 실패가 없다. 다른 패키지는 통과했고, 이번 사이드 질문 관련 케이스와 race 검사도 통과했다. 기준 커밋의 문제를 이번 검수 통과로 표시하지 않았으며, 숨은 문제를 가리려고 기존 단언을 수정하지도 않았다.

Next.js 빌드는 기존에 있던 다중 lockfile / workspace root 추정 경고를 출력한다; 빌드는 완료되었고 모든 페이지가 생성되었다.

## 브라우저 검사

Codex In-app Browser로 독립된 로컬 Go 서비스와 Next.js 개발 서버에 연결했다. 데스크톱과 390 × 844 좁은 화면에서 다음 수동 자동화 작업을 수행하고, 스크린샷과 브라우저 로그를 확인했다:

- 일반 채팅이 실행되는 동안 `/btw`를 입력하면 메인 내용과 사이드 질문이 동시에 표시된다; 데스크톱 사이드바가 정상이다.
- 이어서 추가 질문; 사이드 질문을 중지하면 이미 생성된 부분이 유지되고 메인 흐름은 계속된다.
- 패널을 닫아도 요청은 계속되며, 다시 열면 완료된 답변이 복원된다; 페이지를 새로 고친 뒤 빈 `/btw`가 기록을 복원한다.
- 좁은 화면의 Drawer에서 입력, 버튼, 기록, 닫기 동작이 정상이며 가로 넘침이 없다.
- 비우기는 확인 대화상자를 쓰고, 비운 뒤 기록은 사라지지만 메인 transcript와 스냅샷은 유지된다.
- 작업의 MainAgent와 두 Worker에 각각 질문하고 전환했을 때, Agent 탭과 기록이 섞이지 않았다.
- 블로킹 로컬 모델 픽스처로 Worker를 계속 실행시켜 두고, Worker 메인 입력창에서 `/btw`를 제출했다; 사이드 질문을 중지한 뒤에도 Worker는 실시간 실행과 자신의 일시정지 버튼을 계속 표시했고, 사이드 질문은 부분 답변을 저장했다.
- 브라우저 에러 / 경고 로그가 비어 있다.

제어 가능한 픽스처는 동시성 시점을 정확히 검증하기 위한 것이며, 실제 모델의 출력 속도에 의존하지 않는다. 디버깅 중 Worker 실행 중 검사가 두 번 유효한 동시성 구간을 만들지 못했다(작업이 이미 끝났거나 답변이 일찍 끝남); 픽스처를 고친 뒤 다시 수행해 통과했으며, 이 초기 시도들은 유효한 통과로 기록하지 않는다.

## 실제 모델 대화

`grok-4.6`을 먼저 탐지했으며, OpenAI 호환 인터페이스는 `http://127.0.0.1:12580/tingly/openai`이다. 탐지 HTTP 200, 모델 이름 `grok-4.6`과 `READY`를 반환했고 2.82초 걸렸다. 우선 후보가 사용 가능했으므로 Tingly `glm` 또는 Zhipu `glm-5.3` 대체 체인은 켜지 않았다; 이 두 대체 서비스는 이번에 검증하지 않았다.

| 시나리오 | 실제 결과 |
| --- | --- |
| 메인 세션 실행 중 자산, 목표, 표식을 질문 | `redhaze.top`, 첫 페이지 읽기 및 요약 목표, `BTW-REAL-0910`을 반환; 사이드 질문 완료, 16.97초 |
| 메인 세션이 첫 페이지 읽기를 마친 뒤 도구 근거를 질문 | WebFetch 200, curl 리다이렉트 301 → 302 → 200, 페이지 제목을 올바르게 인용; 7.24초 |
| 사이드 질문이 Bash로 테스트 파일 생성을 요구 | 실행을 거부했고 대상 파일은 생성되지 않음; 7.74초 |
| 완료된 사이드 질문이 메인 컨텍스트를 바꾸지 않음 | 메인 transcript의 SHA-256과 메인 활동 기록이 일치; 사이드 질문의 도구 실행 횟수는 0 |
| Go 서비스를 실제로 중지 / 재시작한 뒤 이어서 질문 | 이전 사이드 기록 3건을 유지하고, 영속화된 스냅샷에서 바로 자산, 표식, 제목을 답했으며 메인 Agent를 다시 실행하지 않음 |
| 새 세션에서 Grok 비스트리밍 설정 사용 | 자산과 `ATOMIC-0910`을 올바르게 답함; 사용량을 반환해 저장: input 11734, output 138, cache_read 11520 |

자산 케이스의 메인 세션은 WebFetch와 Bash/curl로 공개 첫 페이지를 읽었고, 랜딩 페이지는 `https://id.redhaze.top/home`, 제목은 "红幕科技 RedHaze Group · 全球综合集团门户"였다. Bash는 응답을 로컬 테스트 파일에 임시 저장했다; 원격에는 쓰기를 하지 않았다. 이 사실은 "사이드 질문은 도구를 실행하지 않았다"와 별도로 검증했다.

메인 transcript 검증값: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**사용량 제한:** Tingly의 Grok 스트리밍 응답은 usage를 반환하지 않았다. 별도로 `stream_options.include_usage=true`를 직접 보내 검증했으며, HTTP 200, 데이터 프레임 12개, usage 프레임 0개였다. 따라서 스트리밍 테스트의 0은 엔드포인트가 사용량을 제공하지 않았다는 뜻이며, 과금이 없다는 뜻으로 해석할 수 없다. 비스트리밍 사용량과 픽스처의 실패 / 취소 사용량은 올바르게 저장되었다.

## Qwen 검토

검토 모델은 `qwen-flash`이며 OpenAI 호환 인터페이스는 `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200이다. 앞의 세 개의 실제 사이드 대화, 메인 세션의 도구 근거, 엔지니어링 단언을 제공했고, `verdict: accept`, `concerns: []`를 반환했다. 답변이 자산, 표식, 페이지 읽기 증거와 일치하며 사이드 질문의 도구 거부가 제약에 부합한다고 판단했다. 검토 사용량: prompt 6625, completion 312, total 6937.

이번 Qwen 검토 범위에는 나중에 추가한 서비스 재시작과 비스트리밍 테스트가 포함되지 않는다. Qwen의 "쓰기 없음"이라는 요약은 지나치게 넓다: 메인 세션의 curl이 로컬 응답 임시 파일을 실제로 만들었으며, 위에 명확히 기록해 두었다. 동시성, 도구 실행 0건, transcript 격리는 엔지니어링 단언으로 판단했고, 모델 검토는 답변 품질 평가를 보조했을 뿐이다.
