"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";

/**
 * 상단바의 "새 버전 있음" 알림: 페이지 전체 로드 시 한 번 조회하고, 업데이트가 있으면 버전 번호 옆에 표시한다.
 * 클릭하면 시스템 설정 페이지의 「버전 및 업데이트」 카드로 바로 이동한다.
 *
 * 백엔드는 GitHub 조회 결과를 30분간 캐시하므로 마운트할 때마다 한 번씩 조회해도 안전하다
 * ——인증 없는 GitHub API는 IP당 시간당 60회뿐이라, 이 캐시가 없으면 탭을 여러 개 열었을 때
 * 할당량이 바닥나서 정작 업데이트하려 할 때 조회가 되지 않는다.
 *
 * 조회 실패는 항상 조용히 넘어간다: 상단바는 오류를 띄우는 곳이 아니며, 사용자가 설정 페이지에서 「업데이트 확인」을 누르면 원인을 볼 수 있다.
 */
export function UpdateBadge() {
  const [latest, setLatest] = React.useState("");

  React.useEffect(() => {
    let alive = true;
    api
      .checkUpdate()
      .then((r) => {
        // has_update에는 "버전 번호 비교 가능" 판단이 이미 포함되어 있어 개발 빌드에서는 이 알림이 뜨지 않는다.
        if (alive && r.has_update && r.latest) setLatest(r.latest.replace(/^v(?=\d)/, ""));
      })
      .catch(() => {
        // 조용히 처리: 네트워크 끊김 / GitHub 호출 제한 때문에 상단바에 오류를 띄워서는 안 된다.
      });
    return () => {
      alive = false;
    };
  }, []);

  if (!latest) return null;

  return (
    <Link
      href="/system/settings"
      title={`새 버전 ${latest} 발견, 클릭하여 업데이트하러 이동`}
      className="inline-flex items-center gap-1.5 rounded-full bg-primary px-2.5 py-1 font-medium text-primary-foreground text-xs transition-opacity hover:opacity-90"
    >
      {/* 깜빡이는 점: 상단바에 요소가 많아 텍스트만으로는 놓치기 쉬우므로 애니메이션으로 한눈에 보이게 한다. */}
      <span className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary-foreground opacity-75" />
        <span className="relative inline-flex size-1.5 rounded-full bg-primary-foreground" />
      </span>
      <ArrowUpCircleIcon className="size-3.5" />
      <span className="hidden sm:inline">새 버전 {latest}</span>
      <span className="sm:hidden">새 버전</span>
    </Link>
  );
}
