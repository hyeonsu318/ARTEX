"use client";

import * as React from "react";

import {
  CheckCircle2Icon,
  DownloadIcon,
  ExternalLinkIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { api, sseUrl } from "@/lib/api";
import type { UpdateCheck, UpdateProgress } from "@/lib/types";

/** 새 버전이 올라올 때까지 기다리는 최대 시간. 한 번의 업그레이드는 프로세스를 세 번 시작해야 하며(스테이징 → 교체 → 새 버전),
 *  매번 초 단위라서 3분이면 느린 디스크와 Docker 컨테이너 재생성까지 충분히 커버한다. */
const RESTART_TIMEOUT_MS = 180_000;

function humanSize(n?: number): string {
  if (!n || n <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export function UpdateCard() {
  const [info, setInfo] = React.useState<UpdateCheck | null>(null);
  const [checking, setChecking] = React.useState(true);
  const [progress, setProgress] = React.useState<UpdateProgress | null>(null);
  // progress와 분리한다: 스테이징이 끝나면 프로세스가 사라지고 SSE가 끊기므로, 이때는 /api/health 폴링으로 전환해야 한다.
  const [restarting, setRestarting] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // quiet는 백엔드 캐시를 우회할지도 함께 결정한다: 페이지에 들어갈 때의 자동 확인은 캐시를 사용하고(상단바가 방금 조회했으므로),
  // 사용자가 직접 「업데이트 확인」을 누르면 원본으로 강제 조회한다. 그렇지 않으면 방금 배포된 버전은 캐시가 만료되어야 보인다.
  const check = React.useCallback((quiet = false) => {
    setChecking(true);
    api
      .checkUpdate(!quiet)
      .then((r) => {
        setInfo(r);
        if (!quiet) {
          if (r.error) toast.error("업데이트 확인 실패: " + r.error);
          else if (r.has_update) toast.success(`새 버전 ${r.latest}을(를) 발견했습니다`);
          else if (r.comparable) toast.success("현재 최신 버전입니다");
        }
      })
      .catch((e) => {
        if (!quiet) toast.error("업데이트 확인 실패: " + (e as Error).message);
      })
      .finally(() => setChecking(false));
  }, []);

  React.useEffect(() => {
    check(true);
  }, [check]);

  // 버전 번호가 바뀔 때까지 /api/health를 폴링한다.
  //
  // 판정 기준은 "연결이 된다"가 아니라 "버전이 바뀌었다"여야 한다: 교체 과정에서 이전 버전이 잠깐 다시 떴다가
  // (그때는 artex.new를 올려놓고 바로 종료하는 역할만 한다) 연결 여부만 보면 성공으로 오판한다.
  const waitForNewVersion = React.useCallback(async (fromVersion: string) => {
    setRestarting(true);
    const deadline = Date.now() + RESTART_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await sleep(2000);
      try {
        const r = await fetch("/api/health", { cache: "no-store" });
        if (r.ok) {
          const j = (await r.json()) as { version?: string };
          if (j.version && j.version !== fromVersion) {
            toast.success(`${j.version}(으)로 업데이트했습니다. 페이지를 다시 불러오는 중`);
            await sleep(800);
            window.location.reload();
            return;
          }
        }
      } catch {
        // 재시작 구간에서 연결되지 않는 것은 예상된 일이므로 계속 폴링한다.
      }
    }
    setRestarting(false);
    toast.error("서비스 재시작 대기 시간이 초과되었습니다. 백엔드 로그를 확인하거나 artex가 start.sh / start.bat으로 시작되었는지 확인하세요.");
  }, []);

  // 업데이트 진행 상황을 구독한다. SSE는 Next의 /api 리라이트를 거치지 않는다(그 계층은 버퍼링하여 이벤트가 나가지 않는다).
  const openStream = React.useCallback(
    (fromVersion: string) => {
      const es = new EventSource(sseUrl("/api/update/stream"));
      es.onmessage = (ev) => {
        let p: UpdateProgress;
        try {
          p = JSON.parse(ev.data) as UpdateProgress;
        } catch {
          return;
        }
        setProgress(p);
        if (p.phase === "failed") {
          es.close();
          setBusy(false);
          toast.error("업데이트 실패: " + (p.error || p.message));
          return;
        }
        if (p.phase === "staged") {
          es.close();
          void waitForNewVersion(fromVersion);
        }
      };
      es.onerror = () => {
        // 프로세스가 종료되면 SSE는 반드시 끊어진다. 이미 재시작 대기 단계에 들어갔다면 정상적인 현상이므로,
        // /api/health 폴링이 계속 판정하도록 넘기면 된다.
        es.close();
      };
      return es;
    },
    [waitForNewVersion],
  );

  const doUpdate = () => {
    if (!info) return;
    const from = info.current;
    const ok = window.confirm(
      `${info.latest}(으)로 업데이트하시겠습니까?\n\n` +
        "업데이트하면 프로그램이 재시작되며, 실행 중인 작업이 중단됩니다.\n" +
        (info.mode === "docker"
          ? "\n주의: 컨테이너 내 업데이트는 프로그램 자체만 교체하며, 이미지 안의 playwright / nmap 등 툴체인은 업데이트하지 않습니다. " +
            "새 버전이 새 도구에 의존한다면 docker compose pull을 사용하세요."
          : ""),
    );
    if (!ok) return;

    setBusy(true);
    setProgress({ phase: "downloading", percent: 0, message: "준비 중…" });
    const es = openStream(from);
    api.applyUpdate().catch((e) => {
      es.close();
      setBusy(false);
      setProgress(null);
      toast.error("업데이트 시작 실패: " + (e as Error).message);
    });
  };

  const doRollback = () => {
    if (!info) return;
    if (
      !window.confirm(
        "이전 버전으로 롤백하시겠습니까?\n\n프로그램이 재시작되며, 실행 중인 작업이 중단됩니다.\n주의: 데이터베이스 구조는 되돌려지지 않으므로, 이전 버전이 새 버전이 기록한 데이터를 인식하지 못할 수 있습니다.",
      )
    )
      return;
    const from = info.current;
    setBusy(true);
    api
      .rollbackUpdate()
      .then(() => {
        toast.success("이전 버전으로 전환했습니다. 재시작 중…");
        void waitForNewVersion(from);
      })
      .catch((e) => {
        setBusy(false);
        toast.error("롤백 실패: " + (e as Error).message);
      });
  };

  const phase = progress?.phase;
  const showProgress = busy || restarting;
  // 다운로드 단계에서만 실제 퍼센트를 얻을 수 있다(Content-Length 기준). 검증/압축 해제/재시작 대기는 모두
  // 소요 시간을 알 수 없는 단계이므로, 진행 막대를 가득 채우고 펄스 애니메이션을 더해 "작업 중이지만 얼마나 더 걸릴지 알 수 없음"을 나타낸다.
  const downloading = !restarting && phase === "downloading";
  const pct = downloading ? Math.max(progress?.percent ?? 0, 0) : 100;

  return (
    // 설정 페이지는 다단 메이슨리 레이아웃이므로, 카드가 스스로 행 간격을 책임지고 열 사이에서 끊기는 것을 금지한다(page.tsx의 주석 참고).
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <DownloadIcon className="size-4" />
          버전 및 업데이트
        </CardTitle>
        <CardDescription>GitHub에서 새 버전을 확인하고 설치합니다. 업데이트하면 프로그램이 재시작되며, 실행 중인 작업이 중단됩니다.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">현재 버전</span>
          <Badge variant="secondary" className="font-mono">
            {info?.current ?? "…"}
          </Badge>
          {info && (
            <>
              <Badge variant="outline" className="font-mono">
                {info.os}/{info.arch}
              </Badge>
              <Badge variant="outline">{info.mode === "docker" ? "Docker" : "독립 실행 프로그램"}</Badge>
            </>
          )}
          {info?.latest && (
            <>
              <span className="text-muted-foreground">최신 버전</span>
              <Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">
                {info.latest}
              </Badge>
            </>
          )}
          {info?.html_url && (
            <a
              href={info.html_url}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:underline"
            >
              변경 내역 <ExternalLinkIcon className="size-3" />
            </a>
          )}
        </div>

        {info?.boot_notice && (
          <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-700 dark:text-amber-400">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.boot_notice}
          </p>
        )}

        {info?.error && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            GitHub에 연결할 수 없습니다: {info.error}
            {"　"}위쪽에서 전역 프록시를 설정한 후 다시 시도하세요.
          </p>
        )}

        {info && !info.comparable && info.reason && <p className="text-xs text-muted-foreground">{info.reason}</p>}

        {info?.has_update && info.asset_available === false && (
          <p className="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
            {info.latest}에는 {info.os}/{info.arch}용 릴리스 패키지({info.asset})가 없어 자동 업데이트할 수 없습니다.
          </p>
        )}

        {info?.has_update && info.asset_available !== false && (
          <p className="text-xs text-muted-foreground">
            <span className="font-mono">{info.asset}</span>을(를) 다운로드합니다
            {info.size ? ` (${humanSize(info.size)})` : ""}. SHA256 검증과 스모크 테스트를 통과한 후에만 교체되며, 실패하면 현재 버전이 자동으로 유지됩니다.
          </p>
        )}

        {info && !info.has_update && info.comparable && !info.error && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <CheckCircle2Icon className="size-3.5 text-emerald-600" />
            현재 최신 버전입니다.
          </p>
        )}

        {info?.mode === "docker" && info.has_update && (
          <p className="text-xs text-muted-foreground">
            Docker에서의 업데이트는 프로그램 자체만 교체하며, 이미지 안의 playwright / nmap 등 툴체인은 업데이트하지 않고,
            <span className="font-mono"> docker compose up -d </span>
            컨테이너를 재생성하면 이미지에 포함된 버전으로 되돌아갑니다. 이미지까지 함께 업그레이드하려면 다음을 실행하세요
            <span className="font-mono"> docker compose pull artex &amp;&amp; docker compose up -d artex</span>
          </p>
        )}

        {showProgress && (
          <div className="space-y-1.5">
            <Progress value={pct} className={downloading ? undefined : "animate-pulse"} />
            <p className="text-xs text-muted-foreground">
              {restarting ? "재시작하여 새 버전을 적용하는 중입니다. 잠시 기다려 주세요(페이지가 자동으로 새로고침됩니다)…" : progress?.message}
            </p>
          </div>
        )}

        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => check(false)} disabled={checking || busy || restarting}>
            <RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />
            업데이트 확인
          </Button>
          <Button
            size="sm"
            onClick={doUpdate}
            disabled={busy || restarting || !info?.has_update || info?.asset_available === false}
          >
            <DownloadIcon className="size-4" />
            {info?.has_update ? `${info.latest}(으)로 업데이트` : "지금 업데이트"}
          </Button>
          {info?.has_backup && (
            <Button variant="ghost" size="sm" onClick={doRollback} disabled={busy || restarting}>
              <RotateCcwIcon className="size-4" />
              이전 버전으로 롤백
            </Button>
          )}
        </div>

        <p className="text-xs text-muted-foreground">
          원클릭 업데이트는 데몬 스크립트가 프로그램을 재시작하는 데 의존합니다. <span className="font-mono">start.sh</span>(Windows는
          <span className="font-mono"> start.bat</span>)로 ARTEX를 시작하세요. artex 실행 파일을 직접 실행하면 프로그램 종료 후 자동으로 다시 시작되지 않습니다.
        </p>
      </CardContent>
    </Card>
  );
}
