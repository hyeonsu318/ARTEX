# api-recon — 참고 매뉴얼

Grep 레시피, `config.json` 템플릿과 트러블슈팅. 모든 grep은 `js/` 디렉터리에 대해 실행. bundle이 한 줄일 때는 먼저 `js-beautify`나 `sed 's/}/}\n/g'`를 쓸 수 있으며, 보통 컨텍스트 윈도우를 둔 raw grep이면 충분합니다.

## 스크립트 설명

`scripts/` 안의 모든 파일은 **참고 템플릿**이며, 실행 전 목표 사이트에 맞게 조정해야 합니다. 일반적인 수정 지점:

| 스크립트 | 흔히 조정할 항목 |
|---|---|
| `harvest_static.py` | endpoint 정규식, webpack/Vite manifest 파싱, 마이크로 프런트엔드 publicPath, 재시도/동시성 |
| `runtime_harvest.js` | neutralize 필드명과 성공 값, stub 매칭 규칙과 body 구조, routes 소스, WS 녹화, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, L3 활성화 여부, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` 파괴적 링크, cookie, depth/max, 동일 도메인 필터 |
| `extract_route_map.py` | `routeMap` / `routeLink` 정규식, KEY 명명 패턴 |
| `build_perm_tree.py` | `userRouteAuth` 파싱, `ROOTS`/`PREFIX_PARENT` 계층 휴리스틱, stub 외부 필드명 |
| `config.json` | 위 모든 사이트별 전용 파라미터의 통합 진입점 |

조정한 파일은 작업 디렉터리(예: `recon/`)에 두는 것을 권장하며, 보고서에 참고 스크립트 대비 구체적인 변경 사항을 명시합니다.

---

## A. 역추적 삼중 게이트

### A1. 렌더링 게이트 — "로그인 여부를 어떻게 판단하는가?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

`isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`와 같은 체인을 찾아, **저장 키**, **컨테이너**(Cookie vs localStorage), **인코딩**을 확정:

| 인코딩 | config 위조 방식 |
|---|---|
| 평문 문자열 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 서명 없음/`alg:none` JWT, 또는 bundle 내 키로 서명 |
| 암호화(SM2/AES/RSA) | 하드코딩된 키 찾기; 렌더링 게이트가 디코딩 가능한 blob만 필요하면 forge 가능; 아니면 정적 분석으로 대체 |

→ `cookies` / `localStorage`에 기록.

### A2. 인터셉터 게이트 — "무엇이 /login으로의 이동을 트리거하는가?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(未登录|请重新登录|登录已过期|unauthorized|登录失效|授权|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

확정할 것: **필드명**, **성공 값**(보통 `0` 또는 `200`), **이동을 트리거하는 실패 값**. junk session으로 검증:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ `neutralize.fields` + `neutralize.success`에 기록.

### A3. 콘텐츠 게이트 — "메뉴/권한은 어디서 오는가?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**2계층 데이터**(흔한 기업용 백엔드 패턴):

| API | 전형적인 payload | 소비 측 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 라우트 가드, 버튼 수준 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 사이드바 메뉴 렌더링 |
| bundle 내 `userRouteAuth` | `{ CODE: { url, name? } }` | code → 프런트엔드 path |
| bundle 내 `routeMap` | `{ KEY: { name, link } }` | 별칭 해석(webpack `o.DASHBOARD`) |

소비 측 코드를 읽고 확인: `getResultTree(tree, permissions)`가 어떻게 필터링하는지, `v-if` / `hasAuth(code)`가 어떤 필드를 검사하는지.

**수동 forge**(소규모 사이트): permissive payload를 구성 → `stubs`.

**완전한 권한 트리 복원**(대규모 사이트, 사이드바/하위 모듈이 여전히 비어있는 경우): **I절** 참고.

---

## B. config.json 템플릿

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

필드 설명:
- `runtimeMode`: `depth`(Puppeteer), `coverage`(browser MCP), `both`
- `cookies[].value` 접두사: `b64json:` → base64(JSON); `json:` → 원본 JSON; 접두사 없음 → 리터럴
- `forward: true`는 실제 요청을 전달하고 코드 필드를 재작성; `false`는 완전 오프라인 stub
- `mockTier`: coverage 모드 preload에서 활성화할 레벨, 예: `L1+L2`, `L1+L2+L3`
- `routes`는 `routes.txt`에서 가져옴; 메뉴를 forge하면 harness가 자동으로 `<a href>`를 추가
- `captureResponses` / `recordWs`는 depth 모드에서만 유효
- `waitUntil`: 대형 SPA에는 `domcontentloaded`를 사용, `networkidle2`는 멈출 수 있어 피함
- `routeTimeout`: 단일 라우트 `page.goto` 타임아웃(밀리초)
- `proxy`: Puppeteer `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY` 환경변수로도 설정 가능

### B1. 이중 stub 템플릿(role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

외부 필드명(`response_code` / `code` / `data`)은 A2 인터셉터 게이트와 일치해야 함; `permissions`는 tree의 모든 leaf code를 커버해야 함.

---

## C. coverage 모드: preload 설정

`scripts/preload.js` 상단의 `CONFIG` 객체를 편집하거나, CDP 주입 전에 교체:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* config.json의 stubs와 동일 */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

검증: `window.__API_RECON_PRELOAD__ === true`이고 pathname이 안정적.

녹화 결과 export:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook 기능

preload(coverage)와 runtime_harvest(depth)에 내장된 브라우저 Hook 기능과 커버리지:

| Hook 기능 | API 발견에 대한 가치 | 커버리지 |
|---|---|---|
| Hook fetch / XHR.open | 요청 URL/메서드 기록 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | Authorization 등 헤더 발견 | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie 읽기 | 세션 키 이름 확인 | ⚠️ 선택적 `observe.storageReads/cookieReads` |
| Vue 라우트 획득 | frontendRoutes 보완 | ✅ `__API_RECON_ROUTES__`(이미 로드된 라우트) |
| Vue 라우트 가드 중화 / 로그인 이동 차단 | 모듈을 펼쳐 API 트리거 | ✅ `neutralizeVueRouter` + 네이티브 이동 중화 |
| React 라우트 획득 | 라우트 보완 | ⚠️ 정적 분석 + 클릭; 전용 Hook 없음 |
| 페이지 이동 차단(로그인 path) | 페이지에 머물며 분석 | ⚠️ 로그인 path만 차단, 비즈니스 네비게이션은 막지 않음 |
| 암호화 라이브러리(CryptoJS/SM 등) Hook | 암호화된 파라미터 → 평문 API body | ❌ 암호화 함수 입력 파라미터를 수동으로 Hook해야 함; 결론은 config에 기록 |
| 안티 디버깅 우회 | 그렇지 않으면 runtime이 API를 기록하지 못함 | ❌ 수동 처리 필요; 정적 분석은 여전히 가능 |

---

## E. Endpoint 추출 정규식(정적 분석 결과가 너무 적을 때)

`harvest_static.py`의 `extract_endpoints`를 완화하거나, 수동으로:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. 트러블슈팅

| 현상 | 원인 → 처리 |
|---|---|
| 정적 API가 매우 적음 | endpoint 방언 불일치 → 정규식 완화(D절) |
| chunk 수가 manifest보다 훨씬 적음 | CSS-only이거나 배포되지 않은 chunk; 404는 이미 재시도됨 |
| runtime이 여전히 로그인 페이지를 표시 | 렌더링 게이트 오류 → A1 재점검: 키 이름, 컨테이너, 인코딩, domain |
| 셸 진입했지만 모듈이 비어있음 | 콘텐츠 게이트 → 메뉴 forge(A3); `routes` path가 잘못됐을 수 있음 |
| 모든 라우트에 bootstrap/locale만 있음 | 권한 코드 불완전 → I절 권한 트리 복원; `role_permissions` + `permissions/all` 이중 stub 확인 |
| 사이드바에 항목은 있지만 하위 페이지가 비어있음 | tree에 중간 노드가 없거나 code가 `userRouteAuth`와 불일치 |
| 모든 API가 로그인으로 이동 | 인터셉터 게이트 → `neutralize` 확인; 중첩 필드는 walk 로직 확장 필요 |
| WS 프레임이 0 | 사용자 상호작용 후에야 subscribe됨; `perRouteMs`를 늘릴 것 |
| 응답 본문이 비어있음 | `forward: true`일 때만 실제 응답이 있음 |
| Chromium 누락 | chromium을 설치하거나 `config.chromium` / `CHROMIUM` 설정 |
| Mock이 많아도 여전히 로그인으로 돌아감 | Hook이 너무 늦게 걸리거나 `location.href` setter가 없음 → document-start + preload |
| 목록이 전부 비어있음 | L3 빈 배열은 정상; Tab/설정/상세를 계속 클릭 |
| Redux action을 라우트로 오인 | get/set/change/clear/toggle/upload가 포함된 내부 path 필터링 |
| Vue가 여전히 로그인으로 이동 | preload가 document-start가 아님 → 주입 시점 수정; 또는 `neutralizeVueRouter: false`일 때 수동으로 가드 제거 |
| 응답에 URL이 있지만 log에 들어가지 않음 | `extractUrlsFromResponse` 활성화; 또는 `__API_RECON_DETAIL__`에서 수동 추출 |
| Authorization 헤더 이름을 모름 | `observe.xhrHeaders` 활성화 또는 DevTools에서 요청 헤더 확인 |
| runtime이 매우 느림 / 타임아웃 | `waitUntil: domcontentloaded`로 변경; `routeTimeout` 낮춤; `networkidle2` 사용 금지 |
| 프록시 연결 실패 | `proxy` / 환경변수 확인; Puppeteer와 curl 프록시 포트 일치 |

---

## G. hardened 대상

서버 측에서 세션을 점진적으로 검증(forge할 수 없는 서명된 cookie, 서버에서 렌더링되어 stub할 수 없는 메뉴)할 때, runtime은 shell 단계에서 멈춥니다. 예상 동작:

- **정적 분석만으로도 endpoint 열거는 충분** — 모듈 path는 코드 안에 있음
- 승인된 경우, **실제 세션**으로 동일한 harness를 실행: `forward: true`, neutralize 불필요, 실제 methods/params/responses 캡처

---

## H. 단일 작업 체크리스트

1. 승인 범위 확인
2. `scripts/harvest_static.py` **읽기** → 목표에 맞게 조정 → 실행 → `api_static.txt`, `routes.txt` 검토
3. **Phase 1b**: path 앵커 확장 윈도우 + 바인딩 레이어 → `param_candidates.json`(J절)
4. A1/A2/A3 역추적 → 사이트별 `config.json` 작성
5. `runtime_harvest.js` / `preload.js` **읽고 조정**한 후 실행
6. `runtimeMode=depth`: `npm install` → 조정된 harvest 스크립트 실행
7. `runtimeMode=coverage/both`: document-start 시점에 조정된 preload 주입 → browser MCP 동적 열거 + **파라미터 트리거 매트릭스**
8. 모듈이 렌더링되지 않음 → **I절 권한 트리 복원** → stub 패치 → 재실행
9. 파라미터 다중 샘플 diff + 오류 역추론 → `params_merged.json`
10. 병합 → `site_map.json` + `api_merged.txt`, 커버리지, 공백, 스크립트 변경점을 정직하게 명시

---

## I. 권한 트리 복원(Phase 4 심화)

단순한 `menus: [{ path, show: true }]` forge가 효과 없고 하위 모듈이 여전히 mount되지 않을 때 사용.

### I1. auth 모듈 위치 확인

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

기록할 것: **권한 API path**, **응답 필드명**, **소비하는 chunk 파일명**.

### I2. routeMap 추출

```bash
python3 scripts/extract_route_map.py recon/js recon/
# recon/route_map.json 산출
```

`[!] no routeMap pattern found`인 경우: `extract_route_map.py`의 정규식을 완화하거나 수동 grep:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 권한 트리 + stub 구축

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

스크립트 로직:
1. `userRouteAuth={MONITOR:{url:...},...}`를 파싱(webpack 별칭 `He=o.DASHBOARD` 포함)
2. `route_map.json`으로 alias → 실제 path 해석
3. code 접두사로 parent 추론(`MONITOR_ALERT` → `MONITOR`)
4. `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json` 출력
5. `--config` 사용 시 `config.json`의 `stubs`와 확장 `routes`에 자동 반영

**목표에 맞게 조정**(스크립트 상단에서):
- `DEFAULT_ROOTS`: 최상위 모듈 code 목록
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent 매핑
- `DEFAULT_EXTRA_PARENT`: 접두사 관계가 아닌 orphan 노드

### I4. stub 일관성 검증

```bash
# permissions 수는 userRouteAuth 항목 수와 ≈ 같아야 함
wc -l recon/perm_codes_all.txt
# routes는 route_map의 모든 link를 커버해야 함
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. runtime 재실행 후 비교

```bash
node recon/runtime_harvest.js recon/config.json
# forge 전후 runtime_api.json 항목 수 비교; /attack, /asset 등에서 모듈 API가 나타나는지 확인
```

| forge 전 | forge 후(성공) |
|---|---|
| 모든 라우트에서 동일한 3–5개 bootstrap | 서로 다른 라우트가 서로 다른 module API를 트리거 |
| `/api/locale/language`만 존재 | `/api/web/...` 모듈 endpoint가 나타남 |
| `routes.txt`에 한 자릿수 라우트만 | route_map에서 가져온 `routes` 80–110개 이상 |

### I6. 여전히 실패할 때

- **coverage 모드**: 사이드바 + Tab 클릭, 권한 게이팅이 상호작용 이후에만 요청을 보낼 수 있음
- **stub 필드**: 실제 API(curl + 실제 session)와 stub의 nesting을 비교
- **추가 가드**: `hasPermission|checkRole|func.` 등 버튼 수준 검사를 grep하여 `role_permissions.permissions` 확장
- **정적 분석 안전망**: 모듈 API path는 여전히 `api_static.txt`에 있으며, runtime은 METHOD/body만 보완; 파라미터는 `param_candidates.json` + 이미 기록된 샘플 유지

---

## J. 파라미터 역추적(Phase 1b / 5b / 5c)

**방법론이며, 범용 스크립트가 아님.** path를 찾을 때는 정규식을, 파라미터를 찾을 때는 앵커 확장 윈도우 + UI 바인딩 체인 + 다중 샘플 diff + 오류 역추론을 사용.

### J1. 앵커 확장 윈도우 — path로부터 조합 객체 찾기

```bash
# Phase 1에서 알게 된 path를 앵커로
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 래퍼 레이어와 전송 형태

```bash
# axios / 통합 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path 파라미터
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 검증 게이트 — 필수 / 포맷 / 열거값

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 바인딩 레이어 — 폼 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime 보완: DevTools → Network → 요청 → **Initiator**(call stack)에서 `fetch`/`send`로부터 위로 조합 함수를 추적.

### J5. 암호화 파라미터

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**암호문에서 필드를 추측하지 말 것** — 암호화 함수의 **입력 파라미터**를 Hook해서, 암호화 전 plaintext payload를 기록; 결론은 `config.json` / `param_candidates.json`에 작성.

### J6. 파라미터 트리거 매트릭스(Phase 3 필수)

모듈마다 작업별로 한 번씩 기록, 요청 body/query를 diff:

| 작업 | 주목할 점 |
|---|---|
| 목록 첫 화면 | 페이지네이션 기본값 |
| 검색 | keyword, filters |
| 고급 필터 | optional 필드 |
| 신규/수정 | 완전한 entity |
| 일괄/내보내기 | `ids[]`, `exportType` |
| 정렬/페이지 넘기기 | `sortField`, `order` |

`param_samples.json` 산출: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 신뢰도 규칙

| 신뢰도 | 조건 |
|---|---|
| **높음** | 정적 callsite + runtime ≥2 샘플 일치 |
| **중간** | 정적만 있거나, runtime 1회뿐 |
| **낮음** | 응답/오류 역추론, 재검증 안 됨 |
| **트리거 대기** | 정적으로 알려진 필드, UI/권한에 도달하지 못함 |

### J8. 상황별 빠른 조합

| 상황 | 순서 |
|---|---|
| REST 목록 페이지 | J1 조합 객체 → J6 4회 diff → J3 rules |
| 신규/수정 폼 | J3 Form name → J4 submit 체인 → runtime 제출 + 의도적으로 비워서 400 확인 |
| GraphQL | J2 variables 선언 → runtime 각 operation별 variables 기록 |
| 암호화된 body | J5 입력 파라미터 Hook → 암호화 전 필드가 실제 params |

### J9. api-recon 단계와의 매핑

| api-recon | 파라미터 recon |
|---|---|
| Phase 1 정적 | J1 앵커 확장 윈도우 |
| Phase 2 A2 인터셉터 | 전역 주입 필드(tenantId, sign) |
| Phase 3 runtime | J6 트리거 매트릭스 + `param_samples.json` |
| Phase 4 권한 트리 | 모듈마다 폼이 다름 → 권한이 충분해야 전체 필드가 트리거됨 |
| Phase 5 병합 | `params_merged.json` + 신뢰도; 단일 샘플로 필수 여부를 확정하지 말 것 |

### J10. 트러블슈팅

| 현상 | 처리 |
|---|---|
| 정적에는 필드명이 있지만 runtime에 전혀 나타나지 않음 | "트리거 대기"로 표시; 권한 트리 보완 / 고급 필터 클릭 / 연동 select의 각 option 확인 |
| 같은 path인데 body 형태가 다름 | 정상 — `action`별로 나누어 기록, schema를 억지로 합치지 말 것 |
| stub 응답이 가짜지만 params를 보고 싶음 | **outbound 요청** body/headers를 볼 것, stub 응답에서 역추론하지 말 것 |
| 400이 nested field를 알려줌 | 외부 래퍼 `data`/`bizData`/`variables`에 주의 |
| GraphQL에서 operation 이름만 보임 | `variables` JSON을 펼칠 것; 정적에서 `$var: Type` 찾기 |

---
