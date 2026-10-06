#!/usr/bin/env bash
# ARTEX 업데이트 스크립트: ① Docker 업데이트(새 이미지를 받아 재생성)  ② 로컬 빌드 업데이트(바이너리 재빌드)
# install.sh와 대응됨: install은 최초 설치를 담당하고, update는 새 버전으로의 업그레이드를 담당.
# DB 마이그레이션은 수동 실행이 필요 없음 — artex는 시작할 때마다 schema.sql을 멱등하게 재실행한다(ADD COLUMN/CREATE
# INDEX IF NOT EXISTS 포함), 그래서 "재시작이 곧 마이그레이션"이다. 데이터(pgdata 볼륨, ./data, ./skills)는 영향받지 않는다.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── 선택: 저장소를 최신 코드로 동기화(compose/스크립트/로컬 빌드 소스 모두 이걸로 업데이트)───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "git 작업 복제본이 아니므로 git pull을 건너뜁니다"; return; }
  [ "$(ask '최신 코드를 받을까요 (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull이 fast-forward 되지 않았습니다(로컬 변경 또는 브랜치 분기) —— 직접 처리 후 다시 시도하세요. 이번에는 현재 코드를 그대로 사용합니다"
  fi
}

# ── ① Docker 업데이트 ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose 를 찾을 수 없습니다. 먼저 ./install.sh 로 설치하세요"
  [ -f .env ] || die "config env 파일을 찾을 수 없습니다. 먼저 ./install.sh 로 최초 배포를 완료하세요"

  # 선택: 지정한 버전 tag로 업그레이드(비우면 config env의 ARTEX_TAG를 그대로 사용, 기본값 latest)
  local tag; tag="$(ask '대상 이미지 tag(엔터 시 config env / latest 유지)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "ARTEX_TAG 를 ${tag} 로 설정했습니다"
  fi

  # artex만 건드린다: postgres는 고정된 16-alpine이라 따라 올릴 필요가 없다(받아봐야 대역폭 낭비일 뿐이고,
  # 메이저 버전 변경은 호환성 위험도 있다). artex가 depends_on postgres를 선언했으므로 서비스명을 지정해
  # up 할 때 pg가 안 떠있으면 자동으로 띄우고, 이미 떠 있으면 그대로 두고 재생성하지 않는다.
  info "새 이미지 받는 중(artex만)…"
  docker compose pull artex
  info "재생성 및 시작(artex 재시작 시 schema 자동 마이그레이션)…"
  docker compose up -d artex
  ok "업데이트 완료 → http://localhost:8787"
  info "로그 확인: docker compose logs -f artex"
  info "이전 이미지 정리(선택): docker image prune -f"
}

# ── ② 로컬 빌드 업데이트 ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go(>=1.26)를 찾을 수 없습니다: https://go.dev/dl/"
  [ -f config.json ] || warn "config.json을 찾을 수 없습니다 — 최초 배포라면 ./install.sh 를 사용하세요"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "프런트엔드 정적 산출물 재빌드 중…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "프런트엔드 내장 단일 바이너리 재빌드 중…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm을 찾을 수 없습니다: **프런트엔드를 내장하지 않은** 백엔드만 빌드합니다(프런트엔드는 별도로 npm run dev 실행 필요)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "빌드 완료 → ./artex"
  warn "실행 중인 artex 프로세스를 재시작해야 적용됩니다(재시작 시 schema가 자동으로 마이그레이션됩니다)"
}

echo "=============================="
echo "  ARTEX 업데이트"
echo "  1) Docker 업데이트(새 이미지를 받아 재생성)"
echo "  2) 로컬 업데이트(go 재빌드)"
echo "=============================="
case "$(ask '선택' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "유효하지 않은 선택입니다" ;;
esac
