---
name: api-recon
description: 웹사이트 API 인터페이스를 수집할 때 이 skill을 호출합니다.
---

# API Recon(프런트엔드 인터페이스 정찰)

**이미 승인받은** 전제 하에, 가능한 한 완전하게 다음을 발견합니다: **백엔드 API**(경로, 메서드, 파라미터, 응답 본문), **프런트엔드 라우트**, **UI 기능 트리거 지점**(Tab, 팝업, 테이블 조작 등).

---

## 경계와 금지 사항(Agent 필독 · 위반 시 경계 이탈)

이 skill은 **API / 파라미터 측면 정찰만** 수행하며, 취약점 발굴이나 침투 공격 단계가 아닙니다.

### 작업 경계

| 범위 | 허용 | 금지 |
|---|---|---|
| **목표** | path, method, 파라미터, 라우트, UI 트리거 지점 열거 | SQLi/XSS/권한 우회/무차별 대입/fuzz 취약점, 패킷 변조 공격, 파괴적 작업 |
| **인증** | Hook + stub/mock으로 **클라이언트** 로그인 게이트 우회 | 사용자에게 계정/비밀번호를 요구하거나 추측; 실제 로그인 폼 제출 시도 |
| **런타임** | 자격 증명 없이 인터페이스를 hook하고, mock 응답으로 SPA를 로그인 후 셸 레이어로 진입시킴 | 실제 백엔드 세션에 의존해야만 계속할 수 있는 흐름 |

### 자격 증명 없는 동적 분석(Phase 3 기본값)

1. `preload.js` / `runtime_harvest.js`를 통해 로그인, 권한, 메뉴 등 bootstrap 인터페이스를 **가로채서 stub**합니다;
2. 비즈니스 조회 인터페이스에 **구조가 올바르고, 비즈니스 코드가 성공이며, 데이터는 비어 있어도 되는** mock body를 반환합니다;
3. 프런트엔드가 백엔드가 없거나 401 환경에서도 로그인 후 페이지를 렌더링하게 하여 더 많은 XHR/fetch/WebSocket을 트리거합니다;
4. **빈 데이터, 빈 테이블, 플레이스홀더 UI는 모두 예상된 결과입니다**—이를 이유로 실제 로그인이나 취약점 테스트로 방향을 바꾸지 마십시오.

**한 줄 요약**: mock으로 프런트엔드 라우트와 컴포넌트 마운트를 열어젖히고, **outbound 요청만 기록**합니다; 백엔드가 무엇을 반환하는지는 중요하지 않으며, 중요한 것은 프런트엔드가 **또 어떤 인터페이스를 호출하는지**입니다.

### 프로세스 절대 금지 사항

| 금지 | 대체 방법 |
|---|---|
| Phase 1 완료 전 메인 entry `index-*.js`를 grep/curl/Read하여 API path 추출 | `OUTDIR/harvest_static.py` 실행 |
| harvest를 대체하는 `extract_apis.py` 등 스크립트를 직접 작성 | `OUTDIR/harvest_static.py`를 수정 후 재실행 |
| 같은 grep/명령이 2회 이상 실패해도 계속 반복 | 전략 변경: tool_logs 읽기, harvest 수정, reference 확인 |
| 게이트 A/B를 건너뛰고 `scripts/` 원본을 바로 실행 | OUTDIR에 복사 후 목표에 맞게 수정 |
| 실제 사용자명/비밀번호, OTP, OAuth 등 인증 수행 | stub/mock 사용(위 내용 참고) |
| "실제 데이터를 얻기 위해"라는 이유로 stub을 건너뛰고 권한 우회/인젝션 테스트 수행 | outbound만 기록, recon 경계에 속함 |
| 삭제, 민감 데이터 내보내기, 대량 쓰기 등 비가역적 작업 | coverage 클릭 역시 동일하게 적용 |
| runtime + 동적 열거를 완료하지 않고 모든 페이지와 인터페이스를 확보했다고 주장 | "완료 정의" 참고 또는 한계를 명시 |
| 파라미터 트리거 매트릭스 + diff를 완료하지 않고 모든 파라미터를 파악했다고 주장 | Phase 3b 매트릭스 + Phase 5 diff |
| 단일 runtime 샘플로 필수/선택을 추론 | 다중 샘플 diff 또는 검증 규칙/오류 역추론 |

---

## 2계층 모델 + 실행 모드

| 계층 | 산출물 | 한계 |
|---|---|---|
| **정적**(JS bundle) | 전체 endpoint 경로, 라우트 초안, 조합 객체 필드 후보 | HTTP 메서드 없음; 파라미터는 Phase 1b 필요; 런타임에서 조합되는 URL은 놓침 |
| **런타임**(활성 세션) | 메서드 + body + 응답 + 동적 URL + WS/SSE; 다중 샘플 diff로 파라미터 보완 | 페이지가 실제로 렌더링되어야 요청이 발생함; 단일 샘플로는 필수/선택 확정 불가 |

| 실행 모드 | 엔진 | 적용 대상 |
|---|---|---|
| **depth** | `runtime_harvest.js`(Puppeteer) | API 목록, METHOD/params/응답 본문, WS/SSE, 재현 가능한 대량 실행 |
| **coverage** | browser + `preload.js` | Tab/팝업/테이블 클릭, 기능 지점 커버리지 심화 |
| **both** | depth 먼저, 그다음 coverage | 가장 완전하지만 시간이 가장 오래 걸림 |

**파라미터 방법론**(범용 스크립트 없음): path는 harvest/정규식 사용; 파라미터는 **앵커 확장 윈도우 + UI 바인딩 체인 + 다중 샘플 diff + 오류 역추론**(grep 레시피는 [reference.md](reference.md) J절 참고).

---

## 완료 정의

다음을 모두 충족해야 recon이 완료되었다고 할 수 있습니다:

- [ ] **정적**: Phase 1 harvest가 `api_static.txt`, `routes.txt`, `js/`를 산출
- [ ] **런타임**: depth 또는 coverage 중 최소 하나; coverage/both는 **Hook이 작동 + 동적 열거 루프** 필요
- [ ] **셸 진입**: 비즈니스 path 접근 시 `/login`이 아님(hash 라우트 주의)
- [ ] **파라미터**: coverage/both가 파라미터 트리거 매트릭스 + `param_samples.json`을 완료; Phase 5가 `params_merged.json`을 병합
- [ ] **심화**(모듈 페이지가 비어있는 경우): Phase 4 권한 트리 복원 후 재실행하여 **module 수준 API**(locale/bootstrap만이 아닌)가 나타날 때까지 반복
- [ ] **인도**: Phase 5 산출물이 모두 갖춰짐(Phase 5 산출물 표 참고); `insert_assets`가 서비스와 엔드포인트 자산을 기록

---

## 스크립트와 게이트

`scripts/`는 참고용 템플릿일 뿐이며, 원본을 그대로 실행해 최종 결과로 삼는 것은 **금지**입니다.

**규칙**: 먼저 읽기 → 목표에 맞게 수정 → `OUTDIR`(예: `recon/`)에 작성 → `CHANGES.md`에 기록; 맞지 않으면 방법론에 따라 다시 작성하고 구조만 참고.

| 게이트 | 시점 | 참고 스크립트 → OUTDIR 사본 | 흔히 수정할 항목 |
|---|---|---|---|
| **A(정적)** | Phase 0 이후, harvest/spider를 **처음** 실행하기 전 | `harvest_static.py` / `spider_mpa.py` | **대부분의 사이트는 기본 regex로 바로 실행 가능**; manifest/방언이 맞지 않을 때만 endpoint 정규식, webpack/Vite `publicPath`, MPA exclude/cookie 수정 |
| **B(런타임)** | Phase 2 이후, depth/coverage 실행 전 | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage 키, neutralize 성공 값, stubs, login 정규식, api 접두사, hash/history |

**SPA 강제 순서**(교환 불가; Phase 번호가 "먼저 탐색 후 스크립트"보다 우선):

| 단계 | 필수 | 금지 |
|---|---|---|
| Phase 0 완료 후 | 다음 Bash 명령 = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | 메인 entry `index-*.js`를 curl/grep/Read(보통 >500KB) |
| 게이트 A | 스크립트 복사 → 필요 시 소폭 수정 → **즉시 실행** | 먼저 수동으로 API를 추출한 후 harvest 여부를 결정 |
| Phase 1 완료 전 | `wc -l`로 산출물 검증; 404면 harvest를 수정 후 재시도 | extract 스크립트를 직접 작성; 다운로드하지 않은 URL을 반복 grep |
| Phase 1b 시작부터 | grep은 `OUTDIR/js/*.js`에만 | 메인 bundle로 harvest를 대체 |

- ✅ `harvest_static.py` 복사 → (선택) regex 수정 → **즉시 실행**
- ❌ 메인 bundle을 curl → 여러 번 grep → 임시 extract 작성 → 마지막에야 harvest
- **MPA**: Phase 0 이후 다음 Bash 명령 = `python3 OUTDIR/spider_mpa.py ...`

---

## 도구와 출력 제약

| 제약 | 설명 |
|---|---|
| 대용량 파일 | >100KB인 `index-*.js`는 컨텍스트에 Read/grep **금지**; OUTDIR 스크립트로 일괄 처리 |
| grep 출력 | 반드시 `\| head -20` 또는 `-m 5` 사용; 대화에는 path 요약만 남기고 bundle 조각을 붙이지 말 것 |
| 검증 | `wc -l`, `ls \| wc -l` 사용; 디렉터리 전체를 Read하지 말 것 |
| regex 초탐색 | 선택적, ≤1회, ≤50KB의 작은 chunk나 HTML에만; 정식 정적 분석은 harvest를 기준으로 함 |
| reference | 레시피/템플릿/트러블슈팅은 [reference.md](reference.md) 참고, 전문을 inline으로 중복하지 말 것 |

---

## 실행 로드맵

```
Phase 0 분류 + OUTDIR
  → 게이트 A → Phase 1 harvest(★ 즉시 실행 ★)
  → Phase 1b 파라미터 역추적
  → Phase 2 인증 삼중 게이트 → config.json
  → 게이트 B → Phase 3 런타임 + 파라미터 매트릭스
  → Phase 4 권한 트리(필요 시) → Phase 3 재실행
  → Phase 5 병합 보고서 + insert_assets가 발견된 모든 서비스, 엔드포인트 API 자산을 일괄 삽입. 어떤 경우에도 발견된 자산을 빠뜨리고 삽입해서는 안 됨
```

순서대로 체크; **이전 항목이 완료되지 않으면 다음 Phase로 진입할 수 없음**.

1. [ ] **Phase 0**: SPA/MPA 초탐색; `OUTDIR` 생성 → [Phase 0](#phase-0--분류)
2. [ ] **게이트 A + Phase 1**: 스크립트 복사 → **즉시** harvest → `wc -l` 검증 → [Phase 1](#phase-1--정적)
3. [ ] **Phase 1b**: 앵커 확장 윈도우 + 바인딩 레이어 → `param_candidates.json` → [Phase 1b](#phase-1b--파라미터-역추적)
4. [ ] **Phase 2**: 인증 삼중 게이트 → `config.json` → [Phase 2](#phase-2--인증-삼중-게이트)
5. [ ] **게이트 B**: runtime 스크립트 조정 → [Phase 3](#phase-3--런타임)
6. [ ] **Phase 3**: depth / coverage / both; 셸 진입 확인; 파라미터 트리거 매트릭스 → `param_samples.json`
7. [ ] **Phase 4**(필요 시): 권한 트리 → stub 패치 → Phase 3 재실행 → [Phase 4](#phase-4--권한-트리-복원)
8. [ ] **Phase 5**: 산출물 병합 + 보고서 + `insert_assets` → [Phase 5](#phase-5--병합과-보고서)

---

## Phase 0 — 분류

진입점 HTML을 가져오고, **`OUTDIR`를 생성**합니다(skill 내 `scripts/`는 수정하지 말 것):

- **SPA**: 빈 셸 + `<div id=app>` + chunk → Phase 1–5
- **MPA**: SSR + `<form>`, endpoint bundle 없음 → 게이트 A 이후:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

`forms.txt`, `links.txt`, `api_inline.txt`를 산출. SPA인데 forms가 거의 0이면 → Phase 1로 전환.

---

## Phase 1 — 정적

[스크립트와 게이트](#스크립트와-게이트) · [도구와 출력 제약](#도구와-출력-제약)을 준수합니다.

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: HTML script 파싱 → webpack/Vite manifest → 모든 lazy chunk 다운로드 → `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` 산출.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk 수 vs manifest: 404가 있으면 harvest를 수정 후 재시도, chunk를 하나씩 수동 curl하지 말 것
- `api_static.txt`가 너무 적으면 → OUTDIR 내 endpoint 정규식을 완화 후 재실행(reference 참고)

### Phase 1b — 파라미터 역추적

path는 Phase 1에서 가져옴; 파라미터 필드는 별도로 recon해야 함. grep 규칙은 [도구와 출력 제약](#도구와-출력-제약) 참고.

**완료 기준**: 주요 인터페이스에 대해 답할 수 있어야 함—필드명, 전송 위치, 타입 추론, 필수 여부, 샘플 값, 신뢰도.

#### 1b.0 — 전송 형태

| 형태 | 파라미터 위치 | 정적 분석 시 우선 확인 |
|---|---|---|
| REST JSON | body + query | path 앵커 옆 `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql 템플릿, `$page: Int` |
| 전통 form | urlencoded | `<form>`, `FormData` |
| 파일 업로드 | multipart | `FormData.append` |
| path 파라미터 | `/user/:id` | 라우트 테이블 + `useParams` / `$route.params` |
| 암호화/서명 | `sign`/`data`에 포함 | 암호화 함수 입력 파라미터 Hook(reference D절) |

산출물: 각 인터페이스에 `transport: query|json|form|graphql|encrypted` 태그.

#### 1b.1 — 앵커 확장 윈도우

이미 알고 있는 path를 앵커로 삼아, 윈도우를 확장하여 조합 객체를 찾음:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 래퍼 레이어 | 파라미터 단서 |
|---|---|
| axios 인스턴스 | `data` / `params` |
| 통합 request | 인터셉터가 전역 필드 주입 |
| OpenAPI 클라이언트 | 생성된 method 시그니처 |
| React Query / SWR | hook의 두 번째 파라미터 |
| Vue composable | composable 입력 파라미터 |

타입 흔적: `yup`/`zod`/rules, `Form.Item name=`, 내장 Swagger.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 바인딩 레이어

```
Form field → onFinish/handleSubmit → transform → API payload
```

| 바인딩 소스 | 기법 |
|---|---|
| 폼 submit | submit을 따라가 → transform → API |
| 테이블 검색 | `getFieldsValue()` → `params` |
| 라우트 | `:id` / `?tab=` |
| 인터셉터 | 전역 `tenantId`, 페이지네이션, sign |
| 열거형 select | `options` → API 열거값 |

DevTools call stack에서 `fetch`/`XHR.send`로부터 위로 조합 함수를 추적.

#### 1b.3 — 조합 삼문(≠ Phase 2 인증 삼중 게이트)

| 질문 | 답해야 할 내용 |
|---|---|
| **조립** | payload를 어디서 build하는지, transform 흔적 |
| **검증** | required, pattern, enum |
| **전송** | path / query / body / multipart / 헤더 |

인터셉터 게이트(Phase 2)에서 전역 주입 필드(Authorization, `X-Tenant-Id`, sign)도 함께 확인.

#### 1b.4 — Phase 3와의 연결

후보 필드는 정적/바인딩 레이어에서 가져옴; **필수/선택/조건부 의존성**은 Phase 3 파라미터 매트릭스 + diff + Phase 5 오류 역추론이 필요.

---

## Phase 2 — 인증 삼중 게이트

`OUTDIR/js/`에서 grep(`head` 사용)하여 `config.json`에 기록(레시피는 reference 참고):

| 게이트 | 질문 | 키워드 |
|---|---|---|
| **렌더링 게이트** | 로그인 여부를 어떻게 판단하는가? | `isLogin`, `getToken`, Cookie/localStorage |
| **인터셉터 게이트** | 무엇이 `/login`으로의 이동을 트리거하는가? | `response_code`, `errno`, axios interceptor |
| **콘텐츠 게이트** | 메뉴/권한은 어디서 오는가? | `menu`, `permission`, `role`, `acl`, `routes` |

localStorage 키 이름을 자격 증명으로 취급하는 것은 금지——chunk/요청 체인에서 확인해야 함.

**출구 = 게이트 B**: 결론을 `config.json`에 반영하고, `OUTDIR/runtime_harvest.js` / `preload.js`를 수정.

### Phase 2b — API 관찰(선택)

OUTDIR 내 `preload.js`를 사용하여 세션 키 이름, Authorization, 중첩 API URL을 확인:

| 설정 | 산출물 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers 관찰 |
| `extractUrlsFromResponse: true` | 응답 내 하위 API |
| `observe.storageReads/cookieReads: true` | config로 역반영 |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

coverage는 매 라운드마다 다음을 export: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — 런타임

게이트 B를 통과해야 함; [경계와 금지 사항](#경계와-금지-사항agent-필독--위반-시-경계-이탈) · 자격 증명 없는 mock 전략을 준수.

`config.json`에 `"runtimeMode": "depth" | "coverage" | "both"` 설정(템플릿은 reference 참고).

### Hook과 stub(depth + coverage 공용)

| 계층 | 범위 | 목적 |
|---|---|---|
| L1 정확 매칭 | auth/권한/bootstrap stub | 첫 화면 인증 통과 |
| L2 역방향 보정 | 모든 JSON 응답 | 미로그인 코드 → 성공 |
| L3 기본값 | L1에 매칭되지 않은 `/api` 등 | 빈 성공 본문, UI를 펼침 |

- **depth**: fake auth + `forward`로 비즈니스 코드 수정 + `stubs`; `routes`(hash/history) 순회; `runtime_api.json` 산출
- **coverage**: **document-start** 시점에 `preload.js` 주입(CDP `addScriptToEvaluateOnNewDocument` 또는 Userscript)

검증: `window.__API_RECON_PRELOAD__`가 존재함; 비즈니스 path가 `/login`으로 돌아가지 않음.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage 동적 열거(필수)

1. 메인 네비게이션/사이드바 — 항목마다 클릭, 네트워크를 1–3초 대기
2. Tab — `role=tab`, `.ant-tabs-tab`
3. 테이블 — 첫 행의 보기/수정/상세
4. 툴바 — 내보내기, 필터, 신규 생성(**비가역적 삭제는 피할 것**)
5. 모듈에 들어갈 때마다 — API/라우트 병합
6. SPA — `routes.txt`에서 커버되지 않은 path에 제어된 `pushState`(MPA는 금지)

**파라미터 트리거 매트릭스**(필수): 모듈마다 작업 유형별로 한 번씩 기록, **다중 샘플 diff**:

| 작업 | 보통 추가되는 파라미터 |
|---|---|
| 목록 첫 화면 | 페이지네이션 + 기본 필터 |
| 검색 클릭 | keyword, filter |
| 고급 필터 | 더 많은 optional 항목 |
| 신규/수정 | 완전한 entity |
| 일괄/내보내기/정렬 | `ids[]`, `exportType`, `sortField` |

**stub 상태에서도 outbound body/headers는 여전히 실제값**——요청을 기준으로 함. 기록 → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + 사이드바 클릭 + `pushState`
- **both**: 먼저 3a depth, 그다음 3b coverage

---

## Phase 4 — 권한 트리 복원

**트리거**: 모듈 페이지가 비어 있음 / 각 라우트가 bootstrap(예: locale)만 호출 → 콘텐츠 게이트 미통과.

| 현상 | 의미 |
|---|---|
| 셸 진입 성공 | 렌더링 게이트 + 인터셉터 게이트 통과 |
| 사이드바 항목 부족/클릭 시 빈 화면 | stub shape 또는 권한 코드가 불완전 |
| 모든 라우트의 API가 동일하고 매우 적음 | `v-if permission`을 통과하지 못함 |
| `routes.txt`가 bundle보다 훨씬 적음 | auth 모듈에서 보완해야 함 |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

전형적인 체인: `role_permissions`(flat codes) + `permissions/all`(tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

중간 산출물: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub 확인: 외부 `response_code`가 인터셉터 게이트와 일치하는지; flat codes와 tree가 정렬되는지; `routes`가 `route_map`의 모든 link를 커버하는지.

`config.json` 업데이트 후 **Phase 3를 재실행**. 대형 SPA는 `waitUntil`, `routeTimeout`, `perRouteMs`를 조정할 수 있음(reference A3/I절 참고).

---

## Phase 5 — 병합과 보고서

### 산출물 표

| 파일 | 단계 | 내용 |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | 정적 bundle과 path |
| `param_candidates.json` | 1b | 정적 파라미터 필드 후보 |
| `config.json` | 2 | 삼중 게이트 + runtime 설정 |
| `runtime_api.json` | 3a | depth 상세 기록(WS/SSE 포함) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | 다중 샘플, 클릭 로그, detail |
| `route_map.json` 등 | 4 | 권한 트리 중간 파일(실행한 경우) |
| `params_merged.json` | 5 | 병합된 파라미터 필드 + 신뢰도 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | 라우트, API, params, 기능 지점, 한계 |
| **insert_assets** | 5 | 모든 서비스, 엔드포인트 자산을 자산 라이브러리에 기록 |

### 5b — 파라미터 병합

`param_samples.json`에서 diff, **범용 병합 스크립트 없음**. 신뢰도 규칙은 reference J7 참고(높음/중간/낮음/트리거 대기).

### 5c — 오류 역추론

승인된 범위 내에서 불완전한 요청을 보내 400을 읽을 수 있음(**파라미터 recon에 속하며, 취약점 테스트가 아님**): `field 'x' is required`, 열거값 오류 등. `data` 래퍼, `variables`, 암호화 전 `bizData`에 주의.

보고서에는 다음을 명시해야 함: runtimeMode, 정적/런타임 API 수, 파라미터 신뢰도, 커버되지 않은 모듈, 참고 스크립트 대비 `CHANGES.md` 요약.

`site_map.json` 권장 구조:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

더 많은 필드와 grep 레시피는 [reference.md](reference.md) 참고.

---

## 일반 설명

- **프레임워크 무관**: webpack/Vite/Angular lazy load 방식은 동일
- **전송**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web은 범위 밖
- **SSR**: 클라이언트 fetch는 기록 가능; RSC/Server Actions는 완전히 열거할 수 없음
- **사각지대**: JSVMP, WASM, HMAC/mTLS 강한 검증 → 정적 분석 + 한계 명시
- **파라미터 사각지대**: 조건부 연동, hidden params, WASM 조합 → "트리거 대기"/"도달 불가"
- **정적 분석은 안전망**: runtime이 막혀도 정적 분석으로 여전히 endpoint를 열거할 수 있음

---

## 추가 자료

- Grep 레시피, `config.json` 템플릿, 트러블슈팅, Hook, 파라미터 역추적 J절, site_map 템플릿: **[reference.md](reference.md)**
- 참고 스크립트 경로는 [스크립트와 게이트](#스크립트와-게이트) 표 참고
