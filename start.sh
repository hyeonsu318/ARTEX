#!/bin/sh
# ARTEX 감시 실행 스크립트 (Linux / macOS / Docker ENTRYPOINT)
#
# 사용법:
#   ./start.sh                       포그라운드 실행(Ctrl-C로 정지)
#   nohup ./start.sh >artex.log 2>&1 &   백그라운드 상주
#   ./start.sh -addr :9000           추가 인자는 그대로 artex에 전달됨
#
# 이 스크립트가 하는 일은 하나뿐: artex를 실행하고, 프로세스 종료 후 종료 코드에 따라 재기동 여부를 결정한다.
#
#   0      사용자가 정상적으로 정지함   → 루프 종료
#   75     프로그램이 재시작을 요청함   → 즉시 재실행(페이지에서 "원클릭 업데이트" 또는 "롤백"을 눌렀을 때)
#   기타   크래시                      → 지연 후 재실행(1→2→4…최대 60초)
#
# 의도적으로 다운로드, SHA256 검증, 교체는 여기서 하지 않는다: 그 로직을 sh와 bat에 각각 두 벌 작성해야 하는데,
# 바로 이 부분이 가장 실수가 있으면 안 되는 단계다 — 실행되지 않는 바이너리로 잘못 교체되면 이 스크립트는
# 충실하게 그것을 계속 재실행할 뿐이라, 결국 사용자가 직접 기기에 접속해 수동으로 복구해야 한다. 그래서 검증/교체는
# 전부 Go 쪽(selfupdate 패키지)에 남겨 artex 자신이 시작 시 완료하도록 하고, 스크립트는 단순하게 유지한다.
set -u

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 실행 파일 $BIN 을 찾을 수 없습니다" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 정지 신호를 artex 본체로 전달한다.
#
# Docker 환경에서는 이것이 필수다: docker stop은 SIGTERM을 PID 1(바로 이 스크립트)에만 보내고
# 자식 프로세스에는 보내지 않는다. 전달해주지 않으면 artex는 신호를 받지 못해 정상 종료를 못 하고,
# 10초 후 SIGKILL로 강제 종료되어 실행 중인 작업이 중간에 그대로 끊긴다.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 신호가 wait를 중단시켜 128보다 큰 값을 반환하게 한다. 이 시점에는 자식 프로세스가
	# 아직 정상 종료 처리 중이므로, 실제 종료 코드를 받으려면 wait를 한 번 더 호출해야 한다.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 정지됨"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 정상 종료"
			exit 0
			;;
		"$RESTART_CODE")
			# 업데이트/롤백 준비 완료: 재실행 후 artex가 시작 시 교체를 완료한다(selfupdate.Bootstrap 참고).
			echo "[artex] 재시작 요청(새 버전 적용)…"
			delay=1
			;;
		*)
			echo "[artex] 비정상 종료 (code=$code), ${delay}초 후 재시작" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
