#!/usr/bin/env bash
# 개발 모드: 백엔드(:8787) + 트래픽 프록시(:8788)와 프런트엔드 next dev(:5173)를 함께 실행.
# 프런트엔드 /api는 백엔드로 리버스 프록시됨; Ctrl-C로 모두 함께 종료.
#
# 단일 바이너리(프런트엔드 내장) 방식은 README의 「단일 바이너리」절을 참고하며, 이 스크립트를 쓰지 않는다.
set -euo pipefail
cd "$(dirname "$0")"

# 종료 시 이 프로세스 그룹 내 모든 자식 프로세스(백엔드 + 프런트엔드)를 함께 종료.
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 백엔드(일반 go run, 프런트엔드 미내장); 동시 work agent 수는 「시스템 설정」에서 구성.
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# 프런트엔드 핫 리로드(Vite/Next dev server, /api는 :8787로 리버스 프록시).
( cd web && npm run dev ) &

echo "[dev] 백엔드 :8787 / 프록시 :8788 / 프런트엔드 http://localhost:5173  (Ctrl-C로 종료)"
wait
