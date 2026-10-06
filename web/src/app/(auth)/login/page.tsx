"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export default function LoginPage() {
  const router = useRouter();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);

  // 약관 맨 아래까지 스크롤해야(스크롤 없이 전체가 표시되는 경우 포함) 「동의」를 클릭할 수 있다.
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // 열릴 때 초기화하고, 내용이 한 화면에 다 들어가 스크롤이 발생하지 않는 경우도 처리한다.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // 이미 로그인되어 있으면 바로 메인 화면으로 이동한다(정적 내보내기에서는 middleware가 이 이동을 대신해 주지 않는다).
    const token = auth.getToken();
    if (token) {
      // localStorage에는 자격 증명이 남아 있는데 cookie는 유실되었을 수 있다. 먼저 동기화한 뒤 새 요청을 보내,
      // 서버 가드나 라우트 캐시가 여전히 checking 상태인 로그인 페이지로 이동시키는 것을 방지한다.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("백엔드 서비스에 연결할 수 없습니다"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
      setError("먼저 《사용 안내》를 읽고 동의해 주세요");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("사용자 이름 또는 비밀번호가 올바르지 않습니다");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        로그인 상태 확인 중…
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">로그인</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">다시 오신 것을 환영합니다. 계속 ARTEX를 사용하려면 비밀번호를 입력하세요</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">사용자 이름</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">비밀번호</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="비밀번호를 입력하세요"
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                다음 내용을 읽었으며 동의합니다:
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  《사용 안내》
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "로그인 중..." : "로그인"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">ARTEX 사용 안내 및 면책 고지</DialogTitle>
              <p className="text-xs text-muted-foreground">
                버전 v1.0 · 시행일 2026-09-18 · 로그인 전에 아래 모든 조항을 끝까지 읽어 주세요
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              본 《사용 안내 및 면책 고지》(이하 "본 고지")는 귀하와 ARTEX
              프로젝트 작성자 및 기여자 간에 본 소프트웨어의 사용과 관련하여 체결되는 약정입니다. 사용 전에 각 조항의 내용을 신중히 읽고 충분히 이해해 주시기 바라며, 특히 굵은 글씨나 색 블록으로 표시된 면책, 책임 제한 및 금지 조항에 유의하십시오.
              <span className="font-medium text-foreground">
                {" "}
                귀하가 본 소프트웨어를 다운로드, 설치, 접속하거나 어떤 방식으로든 사용하는 즉시, 본 고지의 모든 구속을 읽고 이해하였으며 이에 동의하여 받아들인 것으로 간주합니다.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                제1조 · 정의 및 오픈소스 라이선스
              </h4>
              <p className="pl-7">
                본 소프트웨어(ARTEX)는 GNU Affero General Public License
                v3.0(AGPL-3.0)에 따라 배포되는 오픈소스 프로그램입니다. 귀하는 해당 라이선스에 따라 본 소프트웨어를 자유롭게 사용, 복제, 수정 및 배포할 수 있습니다. 다만 모든 파생 저작물(네트워크를 통해 제3자에게 제공되는 온라인 서비스 포함)은 마찬가지로
                AGPL-3.0 라이선스로 오픈소스화하고 사용자에게 해당하는 전체 소스 코드를 공개해야 합니다. AGPL-3.0의 전체 조항은 함께 제공되는 LICENSE 파일을 따릅니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                제2조 · 허용되는 사용 범위
              </h4>
              <p className="pl-7">
                본 소프트웨어는 개인 학습, 코드 연구, 보안 기술 원리 논의, 그리고 귀하가 직접 구축한 로컬 격리 환경에서의 기술 검증 용도로만 제공되며, 학습, 학술 연구, 코드 검토 등 비공격적·비파괴적 용도에 적합합니다. 본 조에서 명시적으로 허용한 경우를 제외하고, 귀하는 본 소프트웨어를 다른 어떤 목적으로도 사용해서는 안 됩니다.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                제3조 · 금지 행위
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  어떠한 웹사이트, 온라인 서비스, 타인 또는 제3자가 소유한 네트워크 연결 시스템에 대해서도 스캔, 탐색, 악용 또는 공격을 수행하는 것을 엄격히 금지합니다(승인을 받았는지, 귀하 자신의 자산인지 여부를 불문함).
                </li>
                <li>본 소프트웨어를 실제 침투 테스트, 공방 대항, 레드·블루팀 훈련 또는 운영 환경에 사용하는 것을 엄격히 금지합니다.</li>
                <li>본 소프트웨어를 불법 침입, 데이터 탈취, 갈취, 서비스 거부(DoS/DDoS) 또는 기타 파괴적·범죄적 활동에 사용하는 것을 엄격히 금지합니다.</li>
                <li>본 소프트웨어 및 그 출력물에 포함된 저작권, 라이선스 또는 보안 안내 정보를 제거, 변조하거나 우회하는 것을 엄격히 금지합니다.</li>
                <li>귀하가 소재한 국가 또는 지역의 법률, 법규 및 감독 규정을 위반하는 모든 행위를 엄격히 금지합니다.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                제4조 · 지식재산권
              </h4>
              <p className="pl-7">
                본 소프트웨어의 저작권 및 관련 지식재산권은 프로젝트 작성자 및 기여자에게 귀속되며, AGPL-3.0
                라이선스가 정하는 범위 내에서 귀하에게 해당 권리가 부여됩니다. 해당 라이선스가 명시적으로 부여한 권리를 제외하고, 본 고지는 명시적이든 묵시적이든 귀하에게 다른 어떠한 권리도 부여하지 않습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                제5조 · 데이터 및 개인정보
              </h4>
              <p className="pl-7">
                본 소프트웨어는 직접 배포할 수 있는 오픈소스 프로그램이며, 작성자는 어떠한 중앙집중형 서비스도 운영하지 않고 귀하의 사용 데이터를 수집하거나 업로드하지도 않습니다. 사용 과정에서 생성, 처리 또는 접하게 되는 모든 데이터는 귀하가 직접 관리하며 그 적법성과 안전성에 대한 책임도 귀하에게 있습니다. 데이터 처리 부적절로 인해 발생하는 모든 결과는 귀하가 직접 부담합니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                제6조 · 법규 준수 및 법적 책임
              </h4>
              <p className="pl-7">
                귀하는 소재한 국가 또는 지역의 네트워크 보안, 데이터 보안, 개인정보 보호, 컴퓨터 범죄 등과 관련된 모든 법률과 법규를 스스로 준수해야 합니다(중국 본토의 경우 《네트워크안전법》《데이터안전법》《개인정보보호법》 및 관련 사법해석을 포함하되 이에 한정되지 않음).
                <span className="font-medium text-foreground">
                  {" "}
                  귀하가 위 법률 및 법규 또는 본 고지의 약정을 위반하여 발생하는 모든 법적 책임과 결과는 귀하 본인이 단독으로 부담하며, 본 소프트웨어의 작성자 및 기여자와는 무관합니다.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                제7조 · 면책 고지 및 책임 제한
              </h4>
              <p className="pl-7">
                본 소프트웨어는 "있는 그대로(AS IS)" 및 "이용 가능한 상태(AS
                AVAILABLE)"로 제공되며, 상품성, 특정 목적에의 적합성, 정확성 및 비침해에 대한 보증을 포함하되 이에 한정되지 않는 어떠한 명시적 또는 묵시적 보증도 하지 않습니다. 관련 법률이 허용하는 최대 범위 내에서, 본 소프트웨어의 작성자 및 기여자는 본 소프트웨어의 사용 또는 사용 불능(사용 방식의 적절성 여부를 불문함)으로 인해 발생하는 모든 직접적, 간접적, 우발적, 특별 또는 결과적 손해에 대해 책임지지 않으며, 여기에는 데이터 손실, 시스템 손상, 업무 중단, 이익 손실 또는 법적 분쟁이 포함되나 이에 한정되지 않습니다.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                제8조 · 약관 변경 및 최종 해석
              </h4>
              <p className="pl-7">
                작성자는 법률 및 법규 또는 프로젝트 발전상 필요에 따라 수시로 본 고지를 갱신할 수 있으며, 갱신된 버전은 프로젝트와 함께 배포되고 공고일부터 효력이 발생합니다. 귀하가 본 소프트웨어를 계속 사용하면 수정된 약관을 받아들인 것으로 간주합니다. 법률이 허용하는 범위 내에서 본 고지의 최종 해석권은 프로젝트 작성자에게 있습니다. 본 고지의 어느 조항이 무효로 판정되더라도 나머지 조항의 효력에는 영향을 미치지 않습니다.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "모든 조항을 확인하셨습니다" : "조항을 맨 아래까지 스크롤한 후 확인해 주세요"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                모든 조항을 읽었으며 동의합니다
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
