@echo off
rem 콘솔을 UTF-8로 전환(아니면 이 파일의 한글/중국어가 GBK 터미널에서 깨짐).
chcp 65001 >nul 2>&1
rem ARTEX 감시 실행 스크립트 (Windows)
rem
rem 사용법:
rem   start.bat                  포그라운드 실행(Ctrl-C로 정지)
rem   start.bat -addr :9000      추가 인자는 그대로 artex에 전달됨
rem
rem 이 스크립트가 하는 일은 하나뿐: artex.exe를 실행하고, 프로세스 종료 후 종료 코드에 따라 재기동 여부를 결정한다.
rem
rem   0      사용자가 정상적으로 정지함   -> 루프 종료
rem   75     프로그램이 재시작을 요청함   -> 즉시 재실행(페이지에서 "원클릭 업데이트" 또는 "롤백"을 눌렀을 때)
rem   기타   크래시                      -> 지연 후 재실행(1->2->4… 최대 60초)
rem
rem 다운로드, SHA256 검증, 교체는 여기서 하지 않으며 모두 artex 자신이 시작 시 완료한다
rem (selfupdate 패키지). 스크립트는 단순하게 유지하며, 자세한 내용은 start.sh 상단 설명 참고.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 실행 파일 %BIN% 을 찾을 수 없습니다 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 정상 종료
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 업데이트/롤백 준비 완료: 재실행 후 artex가 시작 시 교체를 완료한다.
	echo [artex] 재시작 요청(새 버전 적용)…
	set /a delay=1
	goto loop
)

echo [artex] 비정상 종료 ^(code=!code!^), !delay!초 후 재시작 1>&2
rem 리다이렉션된 콘솔에서는 timeout이 실패하므로 ping으로 대체(N초 지연에는 N+1회 필요).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
