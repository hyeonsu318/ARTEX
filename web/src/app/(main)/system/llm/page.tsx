"use client";

import * as React from "react";

import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile, LLMRetryOverride } from "@/lib/types";
import { cn } from "@/lib/utils";

import { ProfileRetryFields, RetryPolicyPanel, ZERO_OVERRIDE } from "./_components/retry";

// 사고 스위치(thinking.type)와 사고 강도(reasoning_effort)는 【서로 독립된】 두 필드이며,
// 각각 따로 설정한다——일부 인터페이스에는 thinking 필드가 없고 강도 매개변수만으로 사고가 활성화되므로 분리해야 한다.
// DB에 저장된 빈 문자열 = 해당 필드를 【보내지 않음】; Radix Select는 빈 value를 받지 않으므로 UI는 "none"
// 센티넬로 보내지 않음을 나타내고, 저장/조회 시 ""와 서로 변환한다(NONE / fromStore / toStore).
const NONE = "none";
const fromStore = (v?: string) => (v ? v : NONE);
const toStore = (v: string) => (v === NONE ? "" : v);
const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "보내지 않음(기본값)" },
  { value: "disabled", label: "끄기" },
  { value: "enabled", label: "켜기" },
];
// 출력 상한에 어떤 요청 필드명을 쓸지(openai 형식에서만 의미가 있음). NONE ↔ "" 도 같은 센티넬 변환을 따른다.
const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens(기본값)" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];
// 나머지 두 형식은 각자 필드명이 고정되어 있어 옵션이 의미가 없으므로, 설명 문구에서 직접 분명히 밝힌다.
const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "상한을 어떤 키로 보낼지. max_tokens가 기본값이며 대부분의 호환 게이트웨이는 이것만 인식한다. OpenAI 공식 추론 모델(o 시리즈 / GPT-5)은 반대로 max_completion_tokens만 인식하며, max_tokens를 받으면 바로 unsupported_parameter 오류가 난다.",
  anthropic: "openai 형식에서만 선택할 수 있다. Anthropic의 필드명은 max_tokens로 고정되어 있다.",
  "openai-responses": "openai 형식에서만 선택할 수 있다. Responses API의 필드명은 max_output_tokens로 고정되어 있다.",
};
const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "보내지 않음(기본값)" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// 구성이 카드에 표시하는 「정상 여부」. Key를 입력하지 않은 구성은 요청 자체를 보낼 수 없으므로 차단보다 먼저 말해야 한다;
// 나머지 상태는 라운드 로빈의 차단 기록에서 온다(라운드 로빈이 꺼져 있으면 새 기록이 생기지 않으므로, 이때 「정상」 = 알려진 장애 없음).
type Health = { label: string; cls: string; hint?: string };
function healthOf(p: LLMProfile, m?: LLMPoolMember): Health {
  if (!p.api_key_hint) {
    return {
      label: "Key 미설정",
      cls: "border-muted-foreground/40 text-muted-foreground",
      hint: "API Key가 없어 호출할 수 없음",
    };
  }
  if (m?.state === "tripped") {
    return {
      label: m.cooldown_secs > 0 ? `차단됨 · ${cooldownText(m.cooldown_secs)}` : "차단됨",
      cls: "border-destructive/50 text-destructive",
      hint: m.last_error,
    };
  }
  if (m?.state === "degraded") {
    return {
      label: `이상 · 실패 ${m.fails}회`,
      cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
      hint: m.last_error,
    };
  }
  return { label: "정상", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
}

// ─────────────────────────────────────────────────────────────────────────────
// 라운드 로빈 설정 서랍
// ─────────────────────────────────────────────────────────────────────────────

function PoolSheet({
  open,
  onOpenChange,
  pool,
  onReload,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  pool: LLMPoolStatus | null;
  onReload: () => Promise<void>;
}) {
  const [busy, setBusy] = React.useState(false);

  // 쿨다운 카운트다운은 백엔드가 계산한 남은 초이다——서랍이 열려 있고 비정상 구성이 있을 때만 주기적으로 가져와서 흘러가게 한다.
  React.useEffect(() => {
    if (!open || !pool?.enabled || !pool.chain.some((m) => m.state !== "ok")) return;
    const t = setInterval(() => void onReload(), 10_000);
    return () => clearInterval(t);
  }, [open, pool, onReload]);

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await onReload();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? "LLM 라운드 로빈을 켰습니다" : "LLM 라운드 로빈을 껐습니다");
      } else {
        toast.success("폴백 설정을 업데이트했습니다");
      }
    } catch (e) {
      toast.error(`설정 실패: ${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      await api.resetLLMPool(id);
      await onReload();
      toast.success(id ? "해당 구성을 복원했습니다" : "모든 구성을 복원했습니다");
    } catch (e) {
      toast.error(`복원 실패: ${(e as Error).message}`);
    }
  }

  const enabled = pool?.enabled ?? false;
  const chain = pool?.chain ?? [];
  // 라운드 로빈에 참여하는 구성원(「라운드 로빈에 참여하지 않음」으로 표시된 것은 제외), 순서가 곧 백엔드가 실제로 시도하는 순서이다.
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 data-[side=right]:sm:max-w-lg">
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            <ZapIcon className="size-4" /> LLM 라운드 로빈 · 장애 조치
          </SheetTitle>
          <SheetDescription>
            켜면 <b>모델이 지정되지 않은</b> Agent가 현재 구성을 사용할 수 없을 때(잔액 부족 / Key 무효 / 호출 제한 /
            서비스 이상) 자동으로 다음 구성으로 전환합니다.
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-6">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">라운드 로빈 사용</Label>
              <p className="text-muted-foreground text-xs">기본값은 꺼짐. 꺼져 있으면 항상 활성 구성만 사용하며, 실패하면 그대로 실패합니다.</p>
            </div>
            <Switch
              checked={enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
              aria-label="LLM 라운드 로빈 스위치"
            />
          </div>

          {enabled && (
            <>
              <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">지정된 모델이 실패해도 폴백</Label>
                  <p className="text-muted-foreground text-xs">
                    기본값은 꺼짐: Agent나 작업이 특정 구성을 지정했다면 그것만 사용하며, 실패하면 그대로 실패합니다(몰래 다른 모델로 바꾸지 않음).
                    켜면 지정된 구성이 실패했을 때도 아래의 라운드 로빈 체인으로 되돌아갑니다.
                  </p>
                </div>
                <Switch
                  checked={pool?.bind_fallback ?? false}
                  disabled={busy}
                  onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
                  aria-label="바인딩된 구성 실패 폴백 스위치"
                />
              </div>

              <Separator />

              <div className="grid gap-2">
                <div className="flex items-center justify-between">
                  <Label className="text-sm">라운드 로빈 순서</Label>
                  {tripped.length > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => void recover()}>
                      <RotateCcwIcon /> 모두 복원
                    </Button>
                  )}
                </div>
                {inChain.length < 2 && (
                  <p className="text-muted-foreground text-xs">
                    현재 사용 가능한 구성이 {inChain.length}개뿐이라 라운드 로빈이 동작하지 않습니다——API Key가 입력되어 있고 라운드 로빈에 참여하는 구성이 최소 2개 필요합니다.
                  </p>
                )}
                {chain.map((m) => {
                  const excluded = m.excluded && !m.active;
                  const order = excluded ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                  return (
                    <div
                      key={m.profile_id}
                      className={cn(
                        "grid gap-1 rounded-lg border p-2.5 text-sm",
                        excluded && "opacity-55",
                        m.state === "tripped" && "border-destructive/40",
                      )}
                    >
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                        <span className="w-5 shrink-0 text-center font-mono text-muted-foreground text-xs">
                          {order ?? "—"}
                        </span>
                        <span className="font-medium">{m.name}</span>
                        {m.active && (
                          <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                            활성
                          </Badge>
                        )}
                        {excluded && <Badge variant="outline">라운드 로빈 제외</Badge>}
                        <div className="ml-auto flex items-center gap-2">
                          {m.state === "tripped" && m.cooldown_secs > 0 && (
                            <span className="text-muted-foreground text-xs">쿨다운 {cooldownText(m.cooldown_secs)}</span>
                          )}
                          {m.state === "degraded" && (
                            <span className="text-muted-foreground text-xs">연속 실패 {m.fails}회</span>
                          )}
                          {m.state !== "ok" && (
                            <Button
                              size="icon"
                              variant="ghost"
                              className="size-7"
                              aria-label="즉시 복원"
                              title="즉시 복원: 차단을 해제하고 다음 호출에서 해당 구성을 재시도"
                              onClick={() => void recover(m.profile_id)}
                            >
                              <RotateCcwIcon className="size-3.5" />
                            </Button>
                          )}
                        </div>
                      </div>
                      <div className="flex flex-wrap items-center gap-x-3 pl-7 text-muted-foreground text-xs">
                        <code className="truncate font-mono">{m.model}</code>
                        {!m.active && <span>우선순위 {m.priority}</span>}
                      </div>
                      {m.last_error && (
                        <p className="truncate pl-7 font-mono text-muted-foreground text-xs" title={m.last_error}>
                          {m.last_error}
                        </p>
                      )}
                    </div>
                  );
                })}
                {chain.length === 0 && (
                  <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                    구성이 없습니다
                  </div>
                )}
              </div>

              <div className="rounded-lg border border-dashed p-3 text-muted-foreground text-xs leading-relaxed">
                활성 구성은 항상 1순위이며, 나머지는 우선순위가 높은 순(각 구성에서 설정)입니다. 어떤 구성이 실패하면 쿨다운에 들어가(60s → 5min →
                30min), 쿨다운 동안은 건너뛰고 복구되면 자동으로 되돌아옵니다. 컨텍스트 윈도우에 현재 요청이 들어가지 않는 구성은 건너뜁니다. 모델이 지정된 Agent와
                작업 기본값은 라운드 로빈에 참여하지 않습니다.
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// 모델 구성 서랍(신규 생성 / 편집이 같은 폼을 공유)
// ─────────────────────────────────────────────────────────────────────────────

function ProfileSheet({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 신규 생성
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const isNew = !profile;
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<"anthropic" | "openai" | "openai-responses">("anthropic");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // 컨텍스트 윈도우(K tokens); 0=기본 200K
  const [thinkingType, setThinkingType] = React.useState(NONE);
  const [effort, setEffort] = React.useState(NONE);
  const [priority, setPriority] = React.useState("0"); // 라운드 로빈 순위; 클수록 먼저
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [streaming, setStreaming] = React.useState(true); // true=스트리밍(기본값); false=비스트리밍
  const [maxTokens, setMaxTokens] = React.useState("0"); // 1회 응답 출력 상한; 0=보내지 않음
  const [maxTokensField, setMaxTokensField] = React.useState(NONE); // 상한에 쓸 필드명; NONE=max_tokens
  const [sessionHeaderKey, setSessionHeaderKey] = React.useState(""); // 사용자 정의 세션 헤더 이름; 비어 있음=보내지 않음
  const [retry, setRetry] = React.useState<LLMRetryOverride>(ZERO_OVERRIDE); // 이 구성의 재시도 덮어쓰기; 모두 0=전역을 따름
  const [testing, setTesting] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  // 열릴 때마다 전달받은 profile로 폼을 다시 채운다(신규 생성이면 기본값으로 초기화). 서랍을 닫았다가 다시 열면
  // 깨끗한 새 시작이 되어 이전 구성의 잔상이 남지 않는다.
  React.useEffect(() => {
    if (!open) return;
    setName(profile?.name ?? "");
    setFormat(profile?.format === "openai" || profile?.format === "openai-responses" ? profile.format : "anthropic");
    setModel(profile?.model ?? "");
    setBaseUrl(profile?.base_url ?? "");
    setProxy(profile?.proxy ?? "");
    setRps(String(profile?.rate_per_second ?? 0));
    setRpm(String(profile?.rate_per_minute ?? 0));
    setCw(String(profile?.context_window_k ?? 0));
    setThinkingType(fromStore(profile?.thinking_type));
    setEffort(fromStore(profile?.reasoning_effort));
    setPriority(String(profile?.priority ?? 0));
    setPoolExclude(profile?.pool_exclude ?? false);
    setStreaming(profile?.streaming ?? true);
    setMaxTokens(String(profile?.max_tokens ?? 0));
    setMaxTokensField(fromStore(profile?.max_tokens_field));
    setSessionHeaderKey(profile?.session_header_key ?? "");
    setRetry(profile?.retry ?? ZERO_OVERRIDE);
    setApiKey("");
    setKeyHint(profile?.api_key_hint ?? "");
    setModels([]);
    setModelsOpen(false);
  }, [open, profile]);

  const profileId = profile ? Number(profile.id) : undefined;

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy, profileId);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`모델 ${r.models.length}개를 불러왔습니다`);
      } else {
        toast.error(`모델 로딩 실패: ${r.error ?? "모델을 가져오지 못했습니다"}`);
      }
    } catch (e) {
      toast.error(`모델 로딩 중 오류: ${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // 구성이 실제로 실행할 사고 매개변수로 테스트하므로, 해당 필드를 지원하지 않는 모델은 여기서 실패하고
      // 작업을 돌릴 때가 되어서야 터지지 않는다. profile id를 전달한다: Key 입력란이 비어 있으면 저장된 Key를 사용한다.
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        toStore(thinkingType),
        toStore(effort),
        profileId,
        streaming,
        sessionHeaderKey.trim(),
      );
      // 응답 내용도 함께 표시한다: 모델이 실제로 말을 했는지 보여야 세션에서 정상 동작하는 것과 같은 일이라고 할 수 있다.
      if (r.ok)
        toast.success(`연결 성공 · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`, {
          description: r.reply ? `응답: ${r.reply}` : undefined,
        });
      else toast.error(`연결 실패: ${r.error ?? "알 수 없음"}`);
    } catch (e) {
      toast.error(`테스트 중 오류: ${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!name.trim() || !model.trim()) {
      toast.error("이름과 모델을 입력하세요");
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile({
        ...(profile ? { id: Number(profile.id) } : {}),
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        thinking_type: toStore(thinkingType),
        reasoning_effort: toStore(effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
        streaming,
        max_tokens: Math.max(0, Number(maxTokens) || 0),
        // 필드명 스위치는 openai(Chat Completions)에서만 의미가 있으며, 다른 형식은 모두 기본값으로 되돌린다;
        // 백엔드도 같은 정규화를 한 번 더 하므로, 여기서는 UI가 서로 모순된 값을 보내지 않게 할 뿐이다.
        max_tokens_field: format === "openai" ? toStore(maxTokensField) : "",
        session_header_key: sessionHeaderKey.trim(),
        retry,
      });
      if (isNew) toast.success(`새로 만들었습니다: ${name.trim()}(카드에서 「활성으로 설정」해야 사용됩니다)`);
      else toast.success(profile?.is_default ? "저장했습니다. 활성 구성은 즉시 적용되며 재시작이 필요 없습니다" : "저장했습니다");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`저장 실패: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? "새 모델 구성" : `편집: ${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                활성 중
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? "새로 만든 뒤에는 자동으로 활성화되지 않으므로, 카드에서 「활성으로 설정」하여 사용하세요."
              : "수정한 뒤 저장을 누르세요. 활성 구성은 저장 후 모든 Agent에 즉시 적용됩니다."}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">이름</Label>
              <Input
                id="p-name"
                placeholder="예: OpenAI 운영"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="grid gap-2">
              <Label>형식</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai" | "openai-responses")}>
                <SelectTrigger>
                  <SelectValue placeholder="형식 선택" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI (Chat Completions)</SelectItem>
                  <SelectItem value="openai-responses">OpenAI (Responses API)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-model">모델</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: 이 Popover의 콘텐츠는 <body>로 portal되어 Sheet의 스크롤 잠금 밖에 있으므로,
                  modal을 주지 않으면 목록이 렌더링은 되지만 스크롤되지 않는다. modal을 주면 자신이 최상위 스크롤 잠금을 가진다. */}
              <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    size="icon"
                    className="shrink-0"
                    disabled={loadingModels}
                    onClick={loadModels}
                    title="API에서 사용 가능한 모델 불러오기"
                  >
                    {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                  </Button>
                </PopoverTrigger>
                {models.length > 0 && (
                  <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                    {models.map((m) => (
                      <button
                        key={m}
                        type="button"
                        className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                        onClick={() => {
                          setModel(m);
                          setModelsOpen(false);
                        }}
                      >
                        {m}
                      </button>
                    ))}
                  </PopoverContent>
                )}
              </Popover>
            </div>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-base-url">Base URL(선택)</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">프록시(선택)</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              LLM 아웃바운드 요청만 이 프록시를 거치며 http/https/socks5를 지원하고, 계정과 비밀번호를 붙일 수 있습니다(예:
              socks5://user:pass@host:port, 비밀번호에 특수 문자가 있으면 URL 인코딩 필요). 비워 두면 프록시를 사용하지 않습니다(직접 연결).
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-session-header">사용자 정의 세션 헤더(선택)</Label>
            <Input
              id="p-session-header"
              className="font-mono"
              placeholder="예: x-session-id(비우면 보내지 않음)"
              value={sessionHeaderKey}
              onChange={(e) => setSessionHeaderKey(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              헤더 이름을 입력하면 모든 요청에 이 HTTP 헤더가 붙고, 헤더 값은 자동으로 <b>현재 세션의 session id</b>로 채워집니다(chat 세션은
              conv-12, worker는 exp3-worker-i87 등). session-id 헤더로 프롬프트 캐시 /
              스티키 라우팅을 하는 게이트웨이용이며, 같은 세션의 여러 턴에서는 안정적이고 다른 세션끼리는 서로 다릅니다. 비워 두면 보내지 않습니다.
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              placeholder={keyHint ? `설정됨(${keyHint}), 비우면 변경 없음` : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="p-rps">초당 속도 제한</Label>
              <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-rpm">분당 속도 제한</Label>
              <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-cw">컨텍스트 윈도우(K)</Label>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">
            속도 제한 0 = 무제한이며 전체 Agent가 공유합니다. 컨텍스트 윈도우 단위는 K(천 token), 0 = 기본 200K, 상한 1000(즉
            1M); 너무 높게 설정하면 압축이 트리거되지 않습니다.
          </p>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  라운드 로빈 우선순위
                </Label>
                <p className="text-muted-foreground text-xs">
                  숫자가 클수록 먼저 선택됩니다. 활성 구성은 항상 1순위이며 이 값과 무관합니다. 우선순위가 같은 구성은 번갈아 맨 앞에 서므로 자연스럽게 할당량이 분산됩니다.
                </p>
              </div>
              <Input
                id="p-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">라운드 로빈 제외</Label>
                <p className="text-muted-foreground text-xs">
                  켜면 장애 조치 대상으로 쓰이지 않습니다(Agent / 작업이 명시적으로 지정해 사용하는 것은 여전히 가능). 「특정 Agent
                  전용이며 다른 쪽이 실패할 때 소모되는 것을 원하지 않는」 비싼 구성에 적합합니다.
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="라운드 로빈 제외" />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">스트리밍 출력 · streaming</Label>
                <p className="text-muted-foreground text-xs">
                  켜면(기본값) 스트리밍 SSE를 사용하여 실행 중 실시간 진행 상황과 실시간 token 카운트를 제공합니다. 끄면 진짜 비스트리밍(stream:false,
                  완전한 응답을 한 번에 반환)으로 동작하며——일부 게이트웨이의 나쁜 SSE 구현(빈 프레임 / 사고 필드 프레임 유실)을 우회할 수 있지만,
                  대신 실행 중 실시간 진행 상황을 잃습니다.
                </p>
              </div>
              <Switch checked={streaming} onCheckedChange={setStreaming} aria-label="스트리밍 출력" />
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-max-tokens" className="text-sm">
                  출력 상한 · max tokens
                </Label>
                <p className="text-muted-foreground text-xs">
                  1회 응답에서 최대 몇 token을 생성할지로, 매 요청과 함께 보내집니다. 0(기본값) = 이 필드를 보내지 않고 서버 기본값에 따릅니다.
                  위의 「컨텍스트 윈도우」와는 별개입니다: 그것은 모델의 총 용량이며 로컬에서 압축 임계값을 계산하는 데만 쓰입니다.
                  너무 작게 설정하면 추론 모델이 사고 단계에서 잘려 답을 한 글자도 내지 못합니다.
                </p>
              </div>
              <Input
                id="p-max-tokens"
                type="number"
                min={0}
                className="w-28 shrink-0"
                value={maxTokens}
                onChange={(e) => setMaxTokens(e.target.value)}
                placeholder="0"
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">상한 필드명</Label>
                <p className="text-muted-foreground text-xs">{MAX_TOKENS_FIELD_HINTS[format]}</p>
              </div>
              <Select
                value={format === "openai" ? maxTokensField : NONE}
                onValueChange={setMaxTokensField}
                disabled={format !== "openai"}
              >
                <SelectTrigger className="w-56 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {MAX_TOKENS_FIELDS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">사고 스위치 · thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  thinking 필드를 보낼지 제어합니다. 보내지 않음=해당 필드를 붙이지 않음(MiniMax 등 이를 지원하지 않는 모델과 호환); 끄기=
                  disabled를 보냄; 켜기=enabled를 보냄. 아래의 강도와는 서로 독립적입니다.
                </p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">사고 강도 · reasoning_effort</Label>
                <p className="text-muted-foreground text-xs">
                  독립적인 강도 단계(OpenAI reasoning_effort / Anthropic output_config.effort). 일부 인터페이스에는 thinking
                  필드가 없고 강도만으로 사고가 활성화되므로, 따로 설정할 수 있으며 사고 스위치를 보내지 않을 수도 있습니다.
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EFFORT_LEVELS.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <ProfileRetryFields value={retry} onChange={setRetry} />
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? "테스트 중…" : "연결 테스트"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "새로 만들기" : "저장"}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  // 서랍의 열림 여부와 내용은 따로 저장한다: 닫을 때 editing은 그대로 유지한다. 그렇지 않으면 닫는 애니메이션 동안 제목이
  // 「X 편집」에서 「새로 만들기」로 깜빡인다. editing = null은 신규 생성을 뜻한다.
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 카드의 상태 배지는 profile id로 라운드 로빈 상태를 가져온다.
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(`활성화했습니다: ${name}`);
      await load();
    } catch (e) {
      toast.error(`활성화 실패: ${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("현재 활성화된 구성은 삭제할 수 없습니다");
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`삭제했습니다: ${p.name}`);
      await load();
    } catch (e) {
      toast.error(`삭제 실패: ${(e as Error).message}`);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
          <p className="text-muted-foreground text-sm">
            모든 Agent가 공유하는 형식 / 모델 / 속도 제한 설정. 카드를 클릭해 편집하며, 별표는 현재 활성 구성입니다.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon /> 라운드 로빈 설정
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                켜짐
              </Badge>
            )}
          </Button>
          <Button size="sm" variant="outline" onClick={() => openEditor(null)}>
            <PlusIcon /> 새로 만들기
          </Button>
        </div>
      </div>

      <Tabs defaultValue="profiles" className="flex-1">
        <TabsList>
          <TabsTrigger value="profiles">모델 구성</TabsTrigger>
          <TabsTrigger value="retry">재시도 및 백오프</TabsTrigger>
        </TabsList>

        <TabsContent value="profiles" className="mt-4">
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
            {profiles.map((p) => {
              const h = healthOf(p, health.get(p.id));
              return (
                // biome-ignore lint/a11y/useSemanticElements: 카드 안에 자체 동작 버튼이 있어 네이티브 <button>을 쓰면 버튼이 중첩된다(잘못된 HTML)
                <Card
                  key={p.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => openEditor(p)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      openEditor(p);
                    }
                  }}
                  className={cn(
                    "cursor-pointer gap-0 py-4 outline-none transition-colors hover:border-foreground/30",
                    p.is_default && "border-amber-400/50 bg-amber-400/5",
                  )}
                >
                  <CardContent className="grid gap-2 px-4">
                    <div className="flex items-start gap-2">
                      <StarIcon
                        className={cn(
                          "mt-0.5 size-4 shrink-0",
                          p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                        )}
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="truncate font-medium text-sm">{p.name}</span>
                          <Badge variant="outline" className="uppercase">
                            {p.format}
                          </Badge>
                          <Badge variant="outline" className={cn("ml-auto", h.cls)} title={h.hint}>
                            {h.label}
                          </Badge>
                        </div>
                        <code className="mt-1 block truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                      </div>
                    </div>

                    <div className="flex flex-wrap gap-x-3 gap-y-0.5 pl-6 text-muted-foreground text-xs">
                      {p.api_key_hint && <span>{p.api_key_hint}</span>}
                      <span>
                        {p.rate_per_second}/s · {p.rate_per_minute}/min
                      </span>
                      {p.proxy && <span className="truncate">프록시 {p.proxy}</span>}
                      {p.reasoning_effort && (
                        <span>사고 {p.reasoning_effort === "off" ? "끔" : p.reasoning_effort}</span>
                      )}
                      {/* 라운드 로빈 관련 두 필드는 라운드 로빈이 켜져 있을 때만 의미가 있으며, 꺼져 있으면 지면을 차지하지 않는다 */}
                      {poolOn &&
                        !p.is_default &&
                        (p.pool_exclude ? <span>라운드 로빈 제외</span> : <span>우선순위 {p.priority ?? 0}</span>)}
                    </div>

                    <div className="mt-1 flex gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        className="flex-1"
                        disabled={p.is_default}
                        onClick={(e) => {
                          e.stopPropagation();
                          void activate(p.id, p.name);
                        }}
                      >
                        {p.is_default ? "활성화됨" : "활성으로 설정"}
                      </Button>
                      <Button
                        size="icon"
                        variant="outline"
                        aria-label="구성 삭제"
                        onClick={(e) => {
                          e.stopPropagation();
                          void remove(p);
                        }}
                      >
                        <Trash2Icon className="text-destructive" />
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              );
            })}
            {profiles.length === 0 && (
              <div className="col-span-full rounded-lg border border-dashed p-10 text-center text-muted-foreground text-sm">
                아직 모델 구성이 없습니다. 오른쪽 위의 「새로 만들기」를 눌러 첫 번째 구성을 만드세요.
              </div>
            )}
          </div>
        </TabsContent>

        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProfileSheet profile={editing} open={editOpen} onOpenChange={setEditOpen} onSaved={() => void load()} />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />
    </div>
  );
}
