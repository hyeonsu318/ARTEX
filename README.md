<div align="center">

# ARTEX

AI 자율 침투 테스트 시스템 (Go 백엔드 + Next.js 프런트엔드)


🌐 **온라인 데모**: [https://artex-demo.vercel.app/](https://artex-demo.vercel.app/)

</div>

---

## 스크린샷 미리보기

> 전체 상호작용은 [온라인 데모](https://artex-demo.vercel.app/)에서 확인할 수 있습니다.

| 대시보드(개요 / Token 소비 / 활동 스트림) | 작업 목록 |
| :---: | :---: |
| ![대시보드](screenshots/dashboard.png) | ![작업](screenshots/tasks.png) |

| 작업 · 실행 과정(세션 / 도구 호출) | 탐색 경로 |
| :---: | :---: |
| ![실행 과정](screenshots/sessions.png) | ![탐색 경로](screenshots/graph.png) |

| 발견 | 자산 |
| :---: | :---: |
| ![발견](screenshots/findings.png) | ![자산](screenshots/assets.png) |

| 자산 커버리지 그래프(force-directed 레이아웃 · 테스트 완료 강조 · 노드 접기/펼치기) |
| :---: |
| ![자산 커버리지 그래프](screenshots/assets_test.png) |

| 트래픽 레코딩 | 휴먼인더루프 대화 |
| :---: | :---: |
| ![트래픽](screenshots/traffic.png) | ![대화](screenshots/chat.png) |

| Agent 관리 | LLM 설정 |
| :---: | :---: |
| ![Agent](screenshots/agents.png) | ![LLM](screenshots/llm.png) |

| 차단 승인 | 백엔드 로그 |
| :---: | :---: |
| ![차단](screenshots/intercept.png) | ![로그](screenshots/logs.png) |


---

## 승인 기록 상세

전역 「승인 기록」, 작업 내 「차단 승인」 및 대화 중 승인 카드 모두 펼쳐서 상세를 볼 수 있습니다. 표시 구조는
[AegisHook의 승인 상세 컴포넌트](https://github.com/RuoJi6/AegisHook/blob/main/web/src/components/CallDetail.vue)를 참고했으며, ARTEX의 컴포넌트와 테마를 그대로 사용합니다:


## 자산 동기화 (ScopeSentry)

[ScopeSentry](https://github.com/Autumn-27/ScopeSentry)에서 자산 데이터를 직접 동기화할 수 있어, 중복 수집을 피할 수 있습니다:

- 「**자산 동기화**」 페이지에서 ScopeSentry의 주소와 API Key를 입력해 데이터 소스를 연결;
- **프로젝트** 또는 **작업** 단위로 동기화할 대상과 자산 종류(도메인 / 서브도메인 / IP / 포트 / 사이트 / 엔드포인트…)를 선택;
- 원클릭으로 가져와 회사 자산 범위에 따라 병합하면, 바로 ARTEX의 자산 그래프에 들어가 agent가 탐색에 사용할 수 있습니다.

---

## 설치

> **PostgreSQL** 데이터베이스가 필요합니다; 탐색을 위해서는 **LLM** 설정이 필요합니다(`ANTHROPIC_API_KEY` 또는 `OPENAI_API_KEY`, UI에서도 설정 가능).

### 방법 1: 원클릭 설치 스크립트 (권장)

```bash
git clone https://github.com/Autumn-27/ARTEX.git
cd ARTEX
./install.sh
```

스크립트가 하는 일: Docker 감지 / 자동 설치 → **① 전부 Docker** 또는 **② 로컬 빌드 실행** 선택:

- **① 전부 Docker**: Postgres 비밀번호를 입력(엔터로 무작위 생성 가능) → 자동으로 `.env` 작성 → `docker compose up -d`.
- **② 로컬 실행**: 데이터베이스 선택(기존 연결 / Docker로 새로 띄움) → `config.json` 생성 → `go` 로 내장 단일 바이너리 빌드 → 시작.

설치 후 **http://localhost:8787** 을 엽니다(처음 접속하면 `/setup` 에서 관리자 비밀번호를 설정).

### 방법 2: Docker Compose (수동)

```bash
git clone https://github.com/Autumn-27/ARTEX.git
cd ARTEX
cp .env.example .env          # POSTGRES_PASSWORD 입력, ANTHROPIC_API_KEY는 선택
docker compose up -d          # autumn27/artex 이미지 + postgres 받아오기
# → http://localhost:8787
```

이미지에는 자주 쓰는 도구(ripgrep/curl/vim/npm/nmap…)가 포함되어 있습니다; `./skills` 와 `./data` 는 바인드 마운트로 영속화됩니다.

원격 MCP는 시스템 설정에서 `http`(Streamable HTTP) 또는 `sse`(구버전 SSE)를 선택할 수 있습니다.
구버전 SSE 서비스는 보통 `GET /sse` 로 이벤트 스트림을 연 뒤, 서비스가 반환하는
`/message?sessionId=...` 로 JSON-RPC 요청을 받습니다; 설정 시 URL을 `/sse` 로 입력하고, 요청 헤더는
`Authorization=Bearer <token>` 형식으로 입력합니다.

### 방법 3: 미리 빌드된 바이너리 다운로드 (Releases)

[Releases](https://github.com/Autumn-27/ARTEX/releases)에서 해당 플랫폼의 zip을 받아 압축을 풀면 `artex` + `start.sh`(Windows는 `start.bat`) + `skills/` + `config.example.json` 이 나옵니다:

```bash
cp config.example.json config.json   # database 연결 정보 입력
./start.sh                           # → http://localhost:8787
```

> `./artex` 를 직접 실행하지 말고 `start.sh` / `start.bat` 으로 시작하세요. 이것은 감시 스크립트로, 프로그램이 종료되면 종료 코드에 따라 재기동 여부를 결정하며, **페이지의 [원클릭 업데이트](#방법-1-페이지-원클릭-업데이트-권장)가 이것을 통해 교체를 완료합니다**. `./artex` 를 직접 실행하면 업데이트 후 다시 떠오르지 않습니다.
> 백그라운드 상주: `nohup ./start.sh >artex.log 2>&1 &`.

### 방법 4: 소스에서 단일 바이너리 빌드

```bash
# 1) 프런트엔드 정적 내보내기
cd web && npm ci && npm run build:static && cd ..
# 2) 내장 디렉터리로 복사
cp -r web/out server/webui/dist
# 3) 빌드 (-tags embedui 를 줘야 프런트엔드가 내장됨)
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex
./start.sh
```

### 방법 5: 크로스 플랫폼 Release 압축 파일 빌드

`build.sh` 는 먼저 프런트엔드를 빌드해 내장시킨 뒤, Go linker로 디버그 정보를 제거하고, 배포 파일을 zip으로 압축합니다. Release 모드는 기본적으로 Linux amd64/arm64, macOS amd64/arm64, Windows amd64 용 zip 패키지를 생성합니다:

```bash
./build.sh --release
# 산출물: dist/artex-0.3.3-*.zip
```

UPX 자가 압축해제 바이너리는 일부 Linux 커널, 가상화 환경 또는 보안 정책과 호환되지 않을 수 있어 기본적으로 비활성화되어 있습니다. `ARTEX_TARGETS` 로 타깃을 직접 지정할 수 있으며, 대상 실행 환경과의 호환성을 확인했다면 `--upx` 를 명시적으로 전달해 바이너리를 더 줄일 수 있습니다:

```bash
ARTEX_TARGETS=linux/amd64,windows/amd64 ./build.sh --release
./build.sh --target linux/amd64 --upx
```

---

## 업데이트

> 업그레이드는 프로그램만 교체하고 데이터는 건드리지 않습니다: Postgres 데이터 볼륨 `pgdata`, `./data`(jwt.key / SQLite 등), `./skills` 는 모두 보존됩니다. **데이터베이스 마이그레이션은 수동으로 실행할 필요가 없습니다** — `artex` 는 시작할 때마다 `schema.sql`(`ADD COLUMN` / `CREATE INDEX IF NOT EXISTS` 포함)을 멱등하게 재실행하므로, 즉 "재시작이 곧 마이그레이션"입니다. 업그레이드 전에는 그래도 `./data` 와 데이터베이스를 먼저 백업하는 것을 권장합니다.

### 방법 1: 페이지 원클릭 업데이트 (권장)

**시스템 설정** 페이지(사이드바 「시스템 설정」 → `/system/settings`)의 **버전 및 업데이트** 카드에서, 서버에 로그인하지 않고도 새 버전을 바로 확인하고 설치할 수 있습니다.

「업데이트」를 누르면: 현재 플랫폼의 배포 패키지 다운로드 → Release의 `SHA256SUMS` 와 대조 → `-h` 로 새 바이너리 스모크 테스트 → `artex.new` 로 임시 저장 → 프로그램 종료 후 `start.sh` / `start.bat` 이 다시 띄워 교체를 완료합니다. 페이지는 새 버전이 올라올 때까지 자동으로 기다렸다가 새로고침됩니다.

- **실패해도 망가진 프로그램이 남지 않습니다**: 검증 또는 스모크 테스트를 통과하지 못하면 임시 저장 파일을 버리고 현재 버전을 계속 실행합니다; 교체된 새 버전이 연속 3회 시작에 실패하면 `artex.old` 로 자동 롤백됩니다(실패한 버전은 조사용으로 `artex.failed` 로 남습니다).
- **언제든 되돌릴 수 있습니다**: 이전 버전은 `artex.old` 로 보존되며, 카드에 「이전 버전으로 롤백」 버튼이 있습니다. 단, 데이터베이스 구조는 롤백되지 않습니다.
- **업데이트는 실행 중인 작업을 중단시킵니다** — 업데이트는 곧 재시작이므로, 유휴 시간에 진행하세요.
- **개발 빌드는 업데이트를 제공하지 않습니다**: 버전 번호가 `dev` 이거나 `git describe` 접미사가 붙어 있으면 비활성화되어, 정식 버전이 로컬에서 디버깅 중인 바이너리를 덮어쓰는 것을 막습니다.
- **Docker에서는 프로그램만 교체하고 이미지는 바꾸지 않습니다**: 이미지 안의 playwright / nmap 등 툴체인은 함께 업그레이드되지 않으며, `docker compose up -d` 로 컨테이너를 재생성하면 이미지에 내장된 버전으로 돌아갑니다. 이미지까지 함께 업그레이드하려면 `docker compose pull artex && docker compose up -d artex` 를 사용하세요.
- GitHub 접속에 프록시가 필요하면 같은 페이지에서 **전역 프록시**를 설정하면 되며, 업데이트 경로도 그것을 사용합니다. 업데이트는 GitHub 도메인에서만 다운로드하며 HTTPS를 강제합니다.

### 방법 2: 원클릭 업데이트 스크립트

```bash
cd ARTEX
./update.sh
```

스크립트는 먼저 선택적으로 `git pull` 로 최신 코드를 받은 뒤, **① Docker 업데이트** 또는 **② 로컬 빌드 업데이트**를 선택하게 합니다(`install.sh` 와 대응):

- **① Docker**: 대상 이미지 tag를 지정할 수 있음(엔터 시 `.env` 의 `ARTEX_TAG` 를 사용, 기본값 `latest`) → `docker compose pull` → `docker compose up -d`(새 이미지로 재시작하면 자동 마이그레이션).
- **② 로컬**: 프런트엔드 정적 산출물 재빌드 → `./artex` 재빌드(완료 후 프로세스를 재시작해야 적용됨).

### 방법 3: Docker Compose (수동)

```bash
cd ARTEX
git pull                       # compose / 스크립트 업데이트(선택)
# 버전 지정: .env 에 ARTEX_TAG=v0.2.0 설정; 설정하지 않으면 latest 사용
docker compose pull artex
docker compose up -d artex     # 새 이미지로 재시작 → schema 자동 마이그레이션
docker image prune -f          # 이전 이미지 정리(선택)
```

### 방법 4: 미리 빌드된 바이너리 (Releases)

[Releases](https://github.com/Autumn-27/ARTEX/releases)에서 새 버전 zip을 받아, 기존 프로세스를 멈춘 뒤 `artex` 와 `skills/` 를 덮어쓰고(`config.json` 과 `data/` 는 유지) 재시작하면 됩니다:

```bash
cp -r <압축 해제한 디렉터리>/skills ./ && cp <압축 해제한 디렉터리>/artex ./
./start.sh
```

### 방법 5: 소스에서 빌드

```bash
git pull
cd web && npm ci && npm run build:static && cd ..
cp -r web/out server/webui/dist
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex
# ./start.sh 재시작
```

---

## 설정

**데이터베이스** (`config.json`, 또는 환경변수 `ARTEX_PG_DSN` 으로 오버라이드):

```json
{
  "database": {
    "host": "127.0.0.1", "port": 5432,
    "user": "artex", "password": "yourpass",
    "dbname": "artex", "sslmode": "disable"
  }
}
```

**LLM**: `export ANTHROPIC_API_KEY=sk-...`(또는 `OPENAI_API_KEY`), UI의 「LLM 설정」 페이지에서도 입력할 수 있습니다.
선택: `ARTEX_LLM_PROVIDER` / `ARTEX_LLM_MODEL` / `ARTEX_LLM_BASE_URL` / `ARTEX_LLM_PROXY`.

**동시성**: 각 작업의 work agent 수는 「시스템 설정」에서 구성합니다(기본값 3).

**자주 쓰는 파라미터**: `./start.sh -addr :8787 -proxy :8788`(`-addr` 는 프런트엔드+API, `-proxy` 는 트래픽 레코딩 프록시). 시작 스크립트는 인자를 그대로 `artex` 에 전달합니다.

### 리버스 프록시 배포 (HTTPS / 443만 개방)

프런트엔드와 API/SSE는 모두 같은 백엔드 포트(기본값 `:8787`)로 제공되며, 실시간 활동 스트림은 기본적으로 **동일 출처** 주소를 사용하므로 **`NEXT_PUBLIC_SSE_BASE` 를 설정할 필요가 없습니다**. 공용 네트워크에는 443만 열고 8787은 내부망에 두면 됩니다.

SSE는 장기 연결 + 지속적인 푸시이므로, 리버스 프록시는 **반드시 버퍼링을 꺼야** 합니다. 그러지 않으면 브라우저는 연결되지만 이벤트를 받지 못합니다(활동 스트림이 계속 로딩 중으로 보임). Nginx 예시:

```nginx
server {
    listen 443 ssl;
    server_name your.domain.com;
    # ssl_certificate / ssl_certificate_key ...

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE 핵심 설정: 버퍼링 끄기, 긴 타임아웃, HTTP/1.1
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
    }
}
```

> SSE가 페이지와 다른 출처(예: 별도 서브도메인)를 써야 할 때만 **빌드 시점**에 `NEXT_PUBLIC_SSE_BASE` 를 설정하세요(이 변수는 `next build` 시 정적 패키지에 고정되며, 컨테이너 실행 시 다시 설정해도 적용되지 않습니다).

---



## 개발

### 수동 취약점 재검증

작업 상세의 「재검증」 탭에서는 해당 작업의 취약점을 페이지별로 선택하고, 지난 결론과 증거를 확인하며 수동으로 재검증을 시작할 수 있습니다. 시작하면 현재 탭이 유지되며, 로딩 아이콘과 「재검증 중」이 표시됩니다; 수정이 확인되면 취약점 상태가 함께 갱신됩니다.

취약점 목록 각 행의 작업 영역에서 「재검증」을 클릭하거나, 취약점 상세의 「취약점 재검증」 영역에서 「재검증 시작」을 클릭해, 선택적으로 수정 버전, 테스트 조건이나 제약을 입력하면, 시스템이 독립된 재검증 Agent 세션을 만들고, 시작 후 현재 페이지를 유지합니다. 목록의 평면 보기, 작업별 그룹 보기, 자산 보기 모두 이 진입점을 지원합니다; 재검증이 실행 중일 때는 로딩 아이콘과 「재검증 중」이 표시되며, 확인하려면 클릭해 해당 세션으로 들어가고, 종료되면 「재검증」으로 돌아갑니다. 재검증은 원래의 스캔 작업을 다시 시작할 필요가 없으며, 결론은 「여전히 재현됨」, 「수정됨」, 「확인 불가」 세 가지로 나뉘고, 매번의 결론, 증거, 세션 링크는 취약점 상세에 저장됩니다.

새 버전 백엔드는 처음 시작할 때 편집 가능한 「취약점 재검증」(`retester`) Agent를 미리 준비해두며, Agent 관리에서 프롬프트, LLM, 실행 예산, 도구를 설정할 수 있습니다. 기본적으로 바인딩된 LLM을 사용하고, 바인딩되지 않았다면 전역 활성 설정을 사용합니다. 재검증 세션이 성공적으로 끝나고 결론이 「수정됨」이면, 시스템은 자동으로 취약점 처리 상태를 「수정됨」으로 변경합니다; 실행 중, 실패, 정지 또는 다른 결론은 기존 상태를 유지합니다. 원본 증거와 보고서는 항상 보존됩니다. 상태 드롭다운 메뉴에서 수동으로 「수정됨」을 선택할 수도 있습니다. 같은 취약점이 재검증 중일 때는 기존 세션을 재사용하며, 정지, 실패 또는 서비스 재시작 후에는 다시 시작할 수 있습니다.

이번 버전의 이력은 취약점 상세와 세션을 통해 확인하며, 아직 취약점 보고서 내보내기나 작업 아카이브 패키지에는 포함되지 않고, 트래픽 패키지에도 자동으로 연결되지 않습니다. 데모 모드에서는 명확히 표시된 모의 레코드만 생성하며, 실제 타깃에 요청을 보내지 않습니다.

### 로컬 실행과 테스트

```bash
./dev.sh    # 백엔드(:8787) + 트래픽 프록시(:8788) + 프런트엔드 next dev(:5173) → http://localhost:5173
```

- 백엔드: `go run ./cmd/artex`(`-tags embedui` 없이는 프런트엔드가 내장되지 않음)
- 프런트엔드: `cd web && npm run dev`(`/api` 는 백엔드로 리버스 프록시되며 핫 리로드 지원)
- 테스트: `go test ./...`
- Mock 미리보기(백엔드 없이): `cd web && NEXT_PUBLIC_MOCK=1 npm run dev`

---

## 시스템 기술 아키텍처

ARTEX는 **LLM 멀티 agent 기반의 자율 침투 시스템**입니다: Go 모놀리식 백엔드(Next.js 프런트엔드 내장) + PostgreSQL, agent 기능은 [`norma`](https://github.com/Autumn-27/norma) SDK가 제공합니다(`agentcore` / `tool` / `permission` / `harness` / `memory` / `transcript`). 핵심은 **듀얼 그래프 아키텍처**와, 그것을 중심으로 한 두 가지 자율성 메커니즘입니다: **worker 간 프로세스 수준 정보 교환**과 **planner의 다중 라운드 공유 todolist를 통한 안정적인 공격 체인**.

### 전체 계층 구조

```mermaid
flowchart TB
  subgraph FE["프런트엔드 Next.js (go:embed로 단일 바이너리에 내장)"]
    UI["대시보드 · 작업 · 자산 · 커버리지 그래프 · 트래픽 · 워크스페이스 · 시스템 설정"]
  end
  subgraph SRV["server (Go net/http)"]
    API["REST /api/*　JWT 인증　SSE"]
    ENG["engine 스케줄링 루프"]
    MGR["Manager　작업/엔진/store 생명주기"]
  end
  subgraph AG["agent (norma SDK)"]
    GO["goals　목표 분해 + 범위 추출"]
    PL["planner　계획자(유일한 의도 생성자)"]
    WK["worker　실행자 ×N"]
    MA["mainagent　휴먼인더루프"]
  end
  subgraph DB["PostgreSQL"]
    AGRAPH["자산 그래프　assets / companies / task_scope"]
    EGRAPH["탐색 그래프　exploration_nodes / anchors / activity"]
  end
  subgraph SUB["지원 서브시스템"]
    PROXY["트래픽 레코딩 프록시　MITM + CA 흔적"]
    GUARD["guard / intercept　도구 승인 게이트"]
    ENR["enrich　DNS / HTTP 비동기 보완"]
    EXT["MCP · skills · memory · report"]
  end

  UI -->|HTTP| API
  API --> MGR --> ENG
  ENG --> PL
  ENG --> WK
  API --> MA
  API --> GO
  PL --> DB
  WK --> DB
  MA --> DB
  GO --> DB
  WK -->|"Bash / HTTP 전체 흔적 기록"| PROXY
  WK --> GUARD
  WK --> ENR
  PL -.-> EXT
  WK -.-> EXT
  MA -.-> EXT
```

| 계층 | 역할 |
| --- | --- |
| **프런트엔드** | Next.js 정적 내보내기, `go:embed` 로 단일 바이너리에 내장; 작업/자산/탐색 경로/커버리지 그래프 시각화, 휴먼인더루프 대화 |
| **server** | `net/http` 라우팅 + JWT 인증 + SSE; `Manager` 가 작업, 엔진, DB store의 생명주기를 관리 |
| **engine** | 작업마다 하나의 `plannerLoop` + N개의 worker goroutine; 의도 수령, 타임아웃/일시정지/drain |
| **agent** | goals / planner / worker / mainagent, `ToolSet` 이 듀얼 그래프를 LLM 도구로 노출 |
| **db** | 듀얼 그래프의 Postgres 저장(pgx); schema는 `go:embed` 로 매 시작마다 멱등하게 테이블 생성 |
| **지원** | 레코딩형 MITM 프록시, 승인 게이트, 비동기 보완, MCP/스킬/메모리/보고서 |

### 듀얼 그래프 아키텍처: 탐색 그래프 + 자산 그래프

시스템은 「**목표가 무엇인지**」와 「**어디까지 테스트했는지**」를 서로 독립적이면서도 앵커로 연결된 두 개의 그래프로 나눕니다:

- **자산 그래프(Asset Graph, 전역 공유)**: 작업 간에 공유되는 하나의 자산 진실 저장소입니다. 노드는 `root_domain / subdomain / ip / service / app / endpoint` 이며 회사에 소속됩니다; 도메인→서브도메인→서비스→엔드포인트의 부모-자식 관계와 중복 제거 키는 모두 프로그램이 계산하며, agent는 원본 정보만 제출합니다.
- **탐색 그래프(Exploration Graph, 작업마다 독립)**: 한 작업의 "사고와 진행" 과정입니다. 노드는 `goal(목표) / intent(의도) / fact(사실) / finding(취약점) / hint(힌트)` 이며, `spawns / derived_from / yields / proves` 등의 엣지로 **계보 체인**을 이루어 "어떤 방향이 어떤 사실에서 파생되어 무엇을 만들어냈는지"를 설명합니다.
- **두 그래프는 앵커로 연결**: `exploration_anchors(node_id, asset_id)` 가 의도/사실/취약점을 구체적인 자산에 고정합니다 — 이를 통해 "탐색 방향"에서 어떤 자산을 공격했는지도 볼 수 있고, "특정 자산"에서 역으로 이번 작업에서 어떤 의도로 테스트되었고 어떤 사실이 나왔는지도 조회할 수 있습니다. 이는 **자산 테스트 커버리지**와 **자산 커버리지 그래프**(범위 내 자산 + 테스트 완료 강조)도 지원합니다.

```mermaid
flowchart LR
  subgraph EG["탐색 그래프 (작업마다 독립 · 진행 체인)"]
    direction TB
    G["goal 목표"]
    I1["intent 의도 A"]
    F1["fact 사실"]
    I2["intent 의도 B"]
    FD["finding 취약점"]
    G -->|spawns| I1
    I1 -->|yields| F1
    F1 -->|derived_from| I2
    I2 -->|proves| FD
  end
  subgraph AG["자산 그래프 (전역 공유 · 진실 저장소)"]
    direction TB
    RD["root_domain"]
    SD["subdomain"]
    SV["service"]
    EP["endpoint"]
    RD --> SD --> SV --> EP
  end
  I1 -. anchor .-> SD
  F1 -. anchor .-> SV
  I2 -. anchor .-> EP
  FD -. anchor .-> EP
```

> 역할 분담: **planner** 는 탐색 그래프의 상황을 읽고 목표를 판단해, 아직 커버되지 않은 새로운 방향이 있을 때만 **의도**를 frontier에 투입합니다; **worker** 는 **의도 하나**를 받아 실제 도구로 실행하고, 새로운 자산/사실/취약점을 두 그래프에 다시 써넣은 뒤 멈춥니다. 자산 그래프는 공유되는 사실이고, 탐색 그래프는 작업마다의 진행 체인입니다.

### 엔진과 의도 생명주기 (한 번의 탐색이 이루는 폐루프)

엔진은 **이벤트 기반**의 폐루프입니다: 그래프가 바뀌면 planner가 깨어나고, planner가 의도를 내려보내면, worker가 의도를 받아 실행한 뒤 다시 써넣고, 그 쓰기가 다음 라운드를 다시 트리거합니다 — 목표가 증명될 때까지(`prove_goal`) 반복됩니다.

```mermaid
sequenceDiagram
  autonumber
  participant EV as 그래프 변경 debounce
  participant P as planner
  participant FR as frontier 의도 큐
  participant W as worker
  participant PX as 레코딩 프록시
  participant DB as 듀얼 그래프 + activity

  EV-->>P: 깨움
  P->>DB: 상황 읽기(graph_overview 미리 가져오기 + coverage/scope)
  P->>FR: 0..N개의 의도를 내려보냄(asset_ids 포함)
  Note over P,FR: 대부분의 깨어남은 0개를 내려보냄 — 새 방향이 없으면 바로 종료
  W->>FR: claimNext 로 의도 하나를 받음
  W->>DB: 의도의 asset_ids 에 해당하는 원본 자산을 초기 정보로 가져옴
  W->>PX: 실제 도구 실행(Kali / Bash / HTTP)
  PX-->>W: 응답(전체 흔적 기록 + CA 검증)
  W->>DB: fact / asset / finding 를 다시 쓰고 매 단계 activity 기록
  DB-->>EV: 그래프 변경
  EV-->>P: 다시 깨움(폐루프)
```

### worker 간 프로세스 수준 정보 교환

깊이 있는 탐색 한 번에서, 많은 가치 있는 관찰(어떤 에러, 어떤 응답, 어떤 숨겨진 파라미터)이 한 worker의 **실행 과정** 중에 나타나지만, 반드시 정식 fact로 기록되지는 않습니다. 중복 작업을 피하고 체인 위의 worker들이 서로의 성과 위에 설 수 있도록, worker는 **work 간 과정 검색** 능력을 갖추고 있습니다:

- `search_all_worker_traces(q)`: **이번 작업의 다른 work의 실행 과정**에서 키워드로 검색합니다(자신의 의도에 해당하는 단계는 자동으로 제외), 일치하는 항목에는 `intent_id` 가 붙습니다;
- `list_worker_traces` / `get_worker_trace(intent_id, step_ids=[…])`: 먼저 어떤 work가 실행되었는지 확인한 뒤, 특정 work의 몇 단계 전체 내용을 가져와 세부 정보를 교환합니다.

이렇게 하면 탐색 그래프에 아직 대응하는 fact가 없어도, 이후의 worker가 다른 worker의 과정에서 나온 관찰을 재사용할 수 있습니다 — **정보가 worker들 사이에서 "실행 과정" 단위로 흐르면서도**, 경계는 변하지 않습니다(각 worker는 여전히 자신이 받은 의도만 수행합니다).

```mermaid
flowchart LR
  WA["worker A (의도 #12)"] -->|"매 단계 activity"| ACT[("탐색 그래프 · activity 과정 저장소")]
  WB["worker B (의도 #34)"] -->|"매 단계 activity"| ACT
  WC["worker C (의도 #56)"] ==>|"1) search_all_worker_traces(q)"| ACT
  ACT ==>|"2) A/B의 단계 적중(자신은 제외)"| WC
  WC ==>|"3) get_worker_trace(id, step_ids)"| ACT
  ACT ==>|"4) 전체 과정 내용 반환"| WC
```

### planner의 다중 라운드 공유 todolist → 안정적인 공격 체인

실제 공격 체인은 보통 **선후 의존 관계가 있는 다단계 시퀀스**입니다(예: 인젝션 지점 발견 → 자격 증명 획득 → 횡이동 → 권한 상승). 이를 한 번에 병렬로 모두 내려보내면 혼란만 생깁니다. 그래서 planner는 **작업별로 보존되고 여러 번의 깨어남에 걸쳐 공유되는 계획 할 일 목록(todolist)**을 유지합니다:

- planner는 이벤트 기반입니다 — 그래프가 바뀌면 깨어나지만, **매번 깨어날 때마다 완전히 새로운 세션**입니다; 공유되는 todolist는 하나의 직렬 익스플로잇 체인을 **한 번만 기록**하고, 이후 여러 라운드에 걸쳐 **의존 관계에 따라 단계적으로 의도를 내려보내게** 해줍니다. 전체 체인을 한 라운드에 모두 펼쳐 미리 전개하지 않습니다;
- 매 라운드는 「선행 단계가 완료되었고 그 의존 fact가 이미 존재하는」 다음 단계에만 의도를 내려보내며, 진행 상황에 따라 목록을 갱신합니다(이미 fact로 충족된 단계는 완료로 표시).

```mermaid
flowchart TB
  subgraph TODO["공유 todolist (작업별 보존 · 여러 깨어남에 걸쳐 상주)"]
    direction LR
    T1["1 인젝션 지점　[완료]"]
    T2["2 자격 증명 획득　[진행 중]"]
    T3["3 횡이동　[선행 대기]"]
    T4["4 권한 상승　[선행 대기]"]
    T1 -.선행 충족.-> T2 -.-> T3 -.-> T4
  end
  R1["1라운드 깨어남　의도① 내려보냄"] --> T1
  R2["2라운드 (①이 fact 생성)　의도② 내려보냄"] --> T2
  R3["3라운드 (②가 fact 생성)　의도③ 내려보냄"] --> T3
```

이렇게 하면 공격 체인이 "이벤트 기반 + 무상태 세션" 환경에서도 **안정적으로 진행되고, 중복되지 않고, 순서가 뒤바뀌지 않습니다** — 이것이 ARTEX가 다단계 익스플로잇 체인을 자율적으로 끝까지 수행할 수 있는 핵심입니다.

---

## 커뮤니티

QR코드를 스캔해 위챗 공식 계정 **SecSentry** 를 팔로우하고, 공식 계정에서 비공개 메시지를 보내면 커뮤니티에 참여할 수 있습니다.

<div align="center">

<img src="screenshots/wx.png" alt="위챗 공식 계정 SecSentry" width="480" />

</div>

---
## 참고

https://github.com/oritera/Cairn


## 라이선스와 책임 한계 고지

### 오픈소스 라이선스

이 프로젝트는 **GNU Affero General Public License v3.0 (AGPL-3.0)** 로 라이선스되며, 전체 조항은 저장소 루트의 [LICENSE](LICENSE) 파일을 참고하세요.

이는 누구나 이 프로젝트를 자유롭게 사용, 수정, 배포할 수 있지만, **파생 저작물도 동일하게 AGPL-3.0으로 공개해야 함**을 의미합니다; 특히 **이 프로젝트를 수정해 네트워크를 통해(예: 온라인 서비스로 배포) 사용자에게 제공하는 경우에도, 해당 사용자들에게 대응하는 전체 소스코드를 공개해야 합니다**.

> ⚠️ **중요 안내**: 오픈소스 라이선스 자체는 소프트웨어의 사용 목적을 제한하지 않습니다. 아래의 「사용 제한」과 「책임 한계 고지」는 저자가 사용자에게 추가로 약속받는 별도의 약정이자 엄중한 선언이므로, 반드시 준수해야 합니다.

**ARTEX는 개인 학습, 코드 연구 및 로컬 기술 검증 용도로만 제공되며, 어떠한 온라인 시스템이나 웹사이트에 대해서도 실제 테스트를 수행하는 데 사용해서는 안 됩니다.**

### 허용되는 사용 범위

- **이 프로젝트의 소스코드를 읽고, 학습하고, 연구하는 용도**로만 사용할 수 있으며, **로컬 격리 환경**에서 기술 원리를 검증하는 데에만 사용할 수 있습니다;
- 개인 학습, 학술 연구, 코드 리뷰 등 비공격적 용도에 적합합니다.

### 금지 사항

- **이 도구를 사용해 어떤 웹사이트, 온라인 서비스 또는 네트워크에 연결된 시스템에 대해 스캔, 탐지, 익스플로잇 또는 공격을 수행하는 것을 엄격히 금지합니다**(승인 여부, 본인 소유 자산 여부와 무관);
- 이 도구를 실제 침투 테스트, 공격/방어 훈련 또는 운영 환경에 사용하는 것을 엄격히 금지합니다;
- 이 도구를 불법 침입, 데이터 탈취, 랜섬웨어, 서비스 거부 또는 그 외 파괴적·범죄적 활동에 사용하는 것을 엄격히 금지합니다;
- 이 도구를 이용해 소재 국가/지역의 법률 법규를 위반하는 행위를 하는 것을 엄격히 금지합니다.

### 법적 준수 책임

사용자는 소재 국가/지역의 네트워크 보안, 데이터 보호, 컴퓨터 범죄 관련 모든 법률 법규를 스스로 준수해야 합니다(중국 본토의 경우 《네트워크 안전법(网络安全法)》, 《데이터 안전법(数据安全法)》, 《개인정보보호법(个人信息保护法)》 및 관련 사법 해석을 포함하되 이에 한정되지 않음). **이 도구의 사용으로 발생하는 모든 법적 책임과 결과는 사용자 본인이 전적으로 부담합니다.**

### 책임 한계 고지

이 프로젝트는 "있는 그대로(AS IS)" 제공되며, 명시적이든 묵시적이든 어떠한 보증도 수반하지 않습니다. 저자와 기여자는 이 도구의 사용(적절한 사용 여부와 무관)으로 인해 발생하는 어떠한 직접적·간접적 손실, 데이터 손실, 시스템 손상 또는 법적 분쟁에 대해서도 책임을 지지 않습니다. **이 프로젝트를 다운로드, 설치 또는 사용함으로써, 귀하는 위의 모든 조항을 읽고 이해하며 동의한 것으로 간주됩니다.**
