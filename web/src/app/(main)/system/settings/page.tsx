"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, KeyboardIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 운영 제약 주입 범위(기본값은 모두 켜짐).
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 실험 기능: noa 컨텍스트 압축(기본값 꺼짐).
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 순수 프런트엔드 환경설정: /api/settings를 거치지 않고 localStorage를 직접 읽고 쓴다.
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("동시 실행 수는 0보다 큰 정수여야 합니다");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("동시 작업 agent 수를 저장했습니다(이후 시작하는 작업부터 적용)");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("Python 인터프리터 설정을 저장했습니다");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "Agent 자동 트래픽 바인딩을 켰습니다" : "Agent 자동 트래픽 바인딩을 껐습니다");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`저장 실패: ${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "noa 컨텍스트 압축을 켰습니다(이후 시작하는 실행부터 적용)" : "noa 컨텍스트 압축을 껐습니다(내장 압축으로 복원)");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`저장 실패: ${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("웹 검색 설정을 저장했습니다");
      })
      .catch((e) => {
        toast.error("저장 실패: " + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("Brave API Key를 저장했습니다");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("Tavily API Key를 저장했습니다");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "출구 프록시를 저장했습니다" : "출구 프록시를 지웠습니다(직접 연결로 변경)");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "전역 프록시를 저장했습니다" : "전역 프록시를 지웠습니다(직접 연결로 변경)");
      })
      .catch((e) => toast.error("저장 실패: " + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`검색 테스트 성공 · ${r.backend}가 결과 ${r.count}건을 반환했습니다`);
        else toast.error("검색 테스트 실패: " + (r.error || "알 수 없는 오류"));
      })
      .catch((e) => toast.error("검색 테스트 실패: " + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">시스템 설정</h1>
        <p className="text-muted-foreground text-sm">전역 런타임 스위치</p>
      </div>

      {/* grid가 아니라 다단: 웹 검색 카드는 나머지보다 몇 배 높고, 높이도 선택한 백엔드에 따라 달라진다(brave/tavily의
          key 입력은 조건부 렌더링이다). grid는 가장 높은 카드에 맞춰 행 전체를 채워 옆에 큰 빈 공간을 남기지만,
          다단은 내용 높이에 따라 자동으로 균형 있게 채운다. 카드 간격은 gap이 아니라 mb로 준다——다단 레이아웃에서는
          column-gap이 열 간격만 담당하므로 행 간격은 자식 요소가 직접 줘야 한다. */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              트래픽 캡처
            </CardTitle>
            <CardDescription>
              켜면 모든 Agent의 HTTP 트래픽이 기록 프록시를 거쳐 전량 DB에 저장되고, Agent에 traffic_search / traffic_get
              도구와 프록시 설정(프롬프트에 프록시 설명 포함)이 주입됩니다.
              <br />
              끄면(기본값) 어떤 트래픽도 기록하지 않습니다: Agent는
              프록시 설정과 트래픽 도구를 받지 <b>않으며</b>, 프롬프트에도 프록시 관련 내용이 <b>포함되지 않습니다</b>. 전환하면 Agent가 즉시 재구성되어 적용됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "켜짐 · 트래픽을 기록하고 프록시를 주입하는 중" : "꺼짐 · 기록하지 않고 프록시도 주입하지 않음"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Agent 자동 트래픽 바인딩
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              기본값은 꺼짐. 켜면 취약점이 저장될 때 트리거되는 보고서 Agent가 기존 HTTP 요청/응답을 대조하고, 해당 트래픽을 연결한 뒤 보고서를 작성합니다.
              <b>패킷을 조회하고 도구를 추가로 호출하면 Token 소모가 늘어납니다.</b>
              <br />
              TCP, 패킷 미캡처 또는 일치하는 트래픽이 없을 때도 정상적으로 보고할 수 있습니다. 이 스위치는 트래픽 캡처, 수동 바인딩, 이미 저장된 증거 조회에 영향을 주지 않습니다. 다음 라운드의 Agent부터
              적용되며, 끄면 새로운 자동 바인딩은 즉시 거부됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "켜짐 · Token 소모가 늘어납니다" : "꺼짐 · 수동 바인딩은 계속 가능"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              전역 프록시
            </CardTitle>
            <CardDescription>
              모든 Agent의 <b>대상 트래픽</b>이 이 프록시를 거쳐 외부로 나갑니다(소스 IP 은닉 / 점프 호스트 경유). <b>http / https / socks5</b>를 지원하며,{" "}
              <code>user:pass</code> 인증을 붙일 수 있습니다. 비우면 직접 연결.
              <br />
              <b>트래픽 캡처</b>를 켜면 기록 프록시의 <b>업스트림</b>으로 동작하고(트래픽은 여전히 전량 DB에 저장된 뒤 이 프록시를 거쳐 나감), 캡처를 끄면 Agent의
              bash / WebFetch 외부 통신에 직접 주입됩니다. 웹 검색 프록시, LLM 프록시와는 서로 독립적입니다.
              <br />
              <b>참고</b>: <b>캡처를 끈</b> 상태에서 socks5는 각 명령줄 도구의 <code>ALL_PROXY</code> 지원에 의존합니다(curl은
              사용 가능하지만 일부 도구는 무시할 수 있음). socks5를 주로 쓴다면 트래픽 캡처를 켜는 것을 권장합니다——이 경로는 MITM이
              직접 다이얼하므로 도구가 인지하지 못하고 안정적으로 적용됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              프록시 주소
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 또는 http://host:port(비우면 직접 연결)"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                저장
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "설정됨 · 모든 대상 트래픽이 이 프록시를 거쳐 나감" : "설정 안 됨 · 대상 트래픽은 직접 연결로 나감"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              운영 제약 주입
            </CardTitle>
            <CardDescription>
              켜면 각 작업의 <b>운영 제약</b>(작업 개요의 「운영 제약」에서 관리하는 allow/deny 항목)을 해당 Agent의
              시스템 프롬프트에 붙여, 탐색 경계를 정합니다(예: 「현재 포트만 테스트」「브루트포스 금지」).
              <br />
              <b>플래너(planner)</b>와 <b>워커(worker)</b>에 주입할지를 각각 제어할 수 있습니다
              ; 기본값은 모두 켜짐. 전환은 즉시 적용되며(다음 라운드에 읽음) Agent를 재구성할 필요가 없습니다. 끄면 해당 Agent는 더 이상 제약을 보지 못합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                플래너(planner)에 주입{injectPlanner ? " · 켜짐" : " · 꺼짐"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                워커(worker)에 주입{injectWorker ? " · 켜짐" : " · 꺼짐"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              실험 기능
            </CardTitle>
            <CardDescription>
              아직 검증 중인 메커니즘으로 기본값은 꺼짐. Agent 동작을 바꾸거나 안정성에 영향을 줄 수 있으니, 영향을 이해한 뒤에 켜세요.
              <br />
              <b>noa 컨텍스트 압축</b>: 모델이 긴 대화 기록을 능동적으로 압축합니다(norma v0.4.0). 켜면 플랫폼에 연결된 네 종류의 Agent(
              <b>플래너 / 워커 / 메인 Agent / 대화</b>)가 noa로 컨텍스트를 관리하도록 바뀌어 내장 압축을 대체하며,
              압축 원문은 작업 디렉터리 아래에 보관되어 되짚어 볼 수 있습니다. 전환은 즉시 적용되며(이후 시작하는 실행부터 적용) Agent를 재구성할 필요가 없고,
              끄면 즉시 내장 압축으로 복원됩니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              noa 컨텍스트 압축{noaCompaction ? " · 켜짐" : " · 꺼짐"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              웹 검색
            </CardTitle>
            <CardDescription>
              이것은 웹 검색의 <b>마스터 스위치 + 소스 설정</b>입니다. 켜야만 <b>각 Agent의 설정</b>에서 개별적으로
              <b>web_search</b> 사용 여부를 선택할 수 있습니다(제목/링크/요약만 반환하고 본문은 가져오지 않으며, 본문 수집은 WebFetch가 담당). 웹 검색은 기록 프록시를 <b>거치지 않으며</b>,
              트래픽 캡처와 독립적입니다.
              <br />
              소스는 <b>DuckDuckGo(ddgs)</b>(Key 불필요), <b>Brave(무료 버전)</b>(Brave API Key 입력 필요),{" "}
              <b>Tavily</b>(Tavily API Key 입력 필요) 또는 <b>DeepSeek</b>(현재 LLM 설정 재사용) 중에서 선택할 수 있습니다. 마스터 스위치가 꺼져 있으면 각
              Agent의 웹 검색 스위치는 사용할 수 없습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "마스터 스위치 켜짐 · 각 Agent 설정에서 개별적으로 켤 수 있음" : "꺼짐 · 어떤 Agent도 웹 검색을 켤 수 없음"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">검색 소스</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="소스 선택" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo(ddgs · 무료, Key 불필요)</SelectItem>
                    <SelectItem value="brave-free">Brave(무료 버전 · Key 필요)</SelectItem>
                    <SelectItem value="tavily">Tavily(Key 필요)</SelectItem>
                    <SelectItem value="deepseek">DeepSeek(공식)</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">DeepSeek 공식 웹 검색</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  이 소스는 <b>현재 활성화된 LLM 설정</b>을 그대로 재사용합니다. 따라서
                  <b>DeepSeek 공식 모델만 지원</b>하며, 해당 설정은 <b>반드시 anthropic 프로토콜을 사용해야</b> 합니다
                  ——DeepSeek의 OpenAI 프로토콜 엔드포인트는 서버 측 검색을 지원하지 않습니다. LLM 설정을 전환하면 이 소스가 무효가 될 수 있습니다.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  다른 소스와 달리 검색은 <b>DeepSeek 서버에서 실행</b>됩니다: 검색할 때마다 모델 호출이 한 번 추가로 소모되고(Token
                  비용 발생), 검색 요청은 <b>위의 출구 프록시를 거치지 않고</b> <b>트래픽 기록에도 남지 않으며</b>, 반환 결과는 <b>제목과 링크뿐</b>입니다
                  (요약 없음). 본문이 필요하면 WebFetch로 가져옵니다.
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  위 조건을 충족하는지는 직접 확인해야 하며, 시스템은 막지 않습니다. 아래 「검색 테스트」 버튼으로 실제로 한 번 실행해 검증할 수 있습니다.
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">설정됨</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "설정됨(비우면 변경 없음)" : "Brave API Key 입력"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    저장
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    Brave를 선택했지만 아직 Key가 설정되지 않았습니다 —— Key를 저장하기 전에는 검색 도구가 켜지지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  무료 버전의 한도는 월 약 2,000회입니다. https://brave.com/search/api/ 에서 Key를 받으세요.
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">설정됨</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "설정됨(비우면 변경 없음)" : "Tavily API Key 입력(tvly-…)"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    저장
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    Tavily를 선택했지만 아직 Key가 설정되지 않았습니다 —— Key를 저장하기 전에는 검색 도구가 켜지지 않습니다.
                  </p>
                )}
                <p className="text-muted-foreground text-xs">https://tavily.com 에서 가입하고 API Key를 받으세요.</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  출구 프록시(선택)
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 또는 socks5://host:port(비우면 직접 연결)"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    저장
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  독립 출구 프록시로, 검색 엔드포인트에 접근할 때만 사용됩니다(VPN/SOCKS 등). 트래픽을 기록하는 MITM 프록시와는 무관하며, 네트워크가 연결되지 않을 때 이 프록시를 통해 접근합니다.
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  현재 설정(소스 + 프록시 + Key)으로 「test」를 실제로 한 번 검색하여 사용 가능한지 검증합니다.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "테스트 중…" : "검색 테스트"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              사용자 정의 스크립트 · Python 인터프리터
            </CardTitle>
            <CardDescription>
              사용자 정의 <b>script</b> 유형 도구가 이것으로 Python을 실행합니다. 부팅 시 자동으로 감지하며(python3 우선), 여기서 venv /
              특정 버전의 절대 경로를 직접 입력할 수 있고, 비워 두면 실행 시 자동 감지합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3(비우면 자동 감지)"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                다시 감지
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                저장
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              작업 동시성 · Work Agent 수
            </CardTitle>
            <CardDescription>
              각 작업이 동시에 실행하는 작업 agent 수(기본값 3). 값이 클수록 동시 탐지가 많아지고 소모도 커집니다. 변경 후
              <b>이후 시작하는 작업부터 적용</b>되며, 실행 중인 작업에는 영향이 없습니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                저장
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              세션 입력창 전송 키
            </CardTitle>
            <CardDescription>
              대화 페이지와 작업 상세의 메인 Agent 세션 입력창이 이 설정을 공유하며, 선택하면 즉시 적용되어 저장할 필요가 없습니다.
              <br />
              이 설정은 <b>이 브라우저에만 저장</b>되며 계정과 동기화되지 않으므로, 브라우저를 바꾸거나 사이트 데이터를 지우면 다시 설정해야 합니다.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              전송 방식
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
