# syntax=docker/dockerfile:1
#
# 실행용 이미지(이미지 안에서 컴파일하지 않음): 자주 쓰는 도구만 설치하고, **미리 빌드한 Linux 단일 바이너리**를 넣는다.
# 바이너리는 CI의 binaries job이 교차 컴파일(순수 Go, QEMU 없음)하여 대상 아키텍처별로
# 빌드 컨텍스트의 dist/<TARGETARCH>/artex 에 둔다. 이렇게 하면 멀티 아키텍처 빌드 시 arm64는 apt 계층만 에뮬레이션하면 되고,
# Next/Go 컴파일은 더 이상 에뮬레이션하지 않아 훨씬 빠르다.
#
# 로컬에서 수동으로 이미지를 빌드할 때는 먼저 바이너리를 직접 준비한다:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 자주 쓰는 도구: ripgrep / curl / vim, 여기에 recon용 상비 도구 세트를 추가(필요에 따라 증감).
# Node는 NodeSource에서 20.x를 설치: bookworm 기본 apt nodejs는 18이고, Playwright는 >=20을 요구한다.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Playwright MCP와 CLI를 미리(전역으로) 설치해, 실행 시 npx로 다시 네트워크 다운로드하지 않게 한다.
# @playwright/mcp: browser MCP는 `npx @playwright/mcp`로 바로 사용(이미 전역 설치되어 있어 -y/@latest 불필요).
# @playwright/cli: playwright-cli를 제공하며, 설치 후 --help로 실행 가능 여부를 확인한다.
# 이어서 playwright(브라우저 관리 기능 제공)를 설치하고, --with-deps로 chromium과 그 시스템 의존성을 미리 받아둔다.
# 이렇게 하면 컨테이너 내 MCP/CLI가 처음 실행될 때 바로 사용 가능하며, 브라우저를 다시 네트워크로 받지 않는다.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 미리 빌드한 해당 아키텍처용 바이너리(dist/amd64/artex 또는 dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# 감시 실행 스크립트: 프로세스 종료 시 종료 코드에 따라 재기동 여부를 결정하며, 페이지의 원클릭 업데이트도 이것으로 교체가 이루어진다.
# 동시에 SIGTERM을 artex로 전달하는 역할도 한다 — docker stop은 신호를 PID 1에만 보내므로,
# 전달해주지 않으면 artex가 신호를 받지 못해 정상 종료를 못 하고 10초 후 SIGKILL로 강제 종료된다.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key) 영속화 지점
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
