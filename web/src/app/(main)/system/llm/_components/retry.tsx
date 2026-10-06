"use client";

// LLM 재시도 설정의 공용 컴포넌트: 5개 계층 각각의 「횟수 + 간격」.
//
// 5개 계층은 안쪽에서 바깥쪽 순서: 연결 수립(SDK) → 빈 응답(SDK) → 동일 provider 안전 구간 → 라운드 로빈 차단 → 의도 재실행.
// 앞의 세 계층은 엔드포인트를 따르므로 모든 모델 구성이 전역 기본값을 덮어쓸 수 있고, 뒤의 두 계층은 프로세스 단위라 전역에 하나뿐이다.
//
// 모든 입력은 동일한 「비워 둠 = 설정하지 않음」 의미를 따르며, 백엔드의 db.RetryRule과 일치한다:
//   횟수   비어 있음/0 = 내장 기본값 사용 | -1 = 이 계층의 재시도 끄기 | >0 = 이 횟수 사용
//   간격   비어 있음/0 = 이 계층 고유의 지수 백오프 사용 | >0 = 이 고정 밀리초 간격 사용

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** 이 재시도가 어디서 발생하고 누가 수행하는지 */
  where: string;
  /** 어떤 오류가 이 계층으로 오는지 —— 상태 코드까지 구체적으로, 추측하게 하지 않는다 */
  trigger: string;
  /** 비슷해 보이지만 이 계층으로【오지 않는】 오류. 입력해도 반응이 없을 때 버그로 오해하지 않도록 한다 */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 횟수를 비워 뒀을 때의 기본값(플레이스홀더용) */
  defAttempts: number;
  /** 간격을 비워 뒀을 때의 기본 전략(플레이스홀더용) */
  defInterval: string;
  /** 횟수에 -1을 입력했을 때의 의미 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "연결 수립 재시도",
    where: "SDK · 200을 받기 전",
    trigger:
      "연결할 수 없거나 아직 200을 받지 못함: 연결 재설정 / 읽기·쓰기 타임아웃 / DNS 실패 등 네트워크 계층 오류와 HTTP 408, 429, 500, 502, 503, 504.",
    skips: "그 외 상태 코드(400 / 401 / 403 / 404 / 413 / 422 등)는 확정적인 거부이므로 다시 보내도 똑같이 실패하여 그대로 위로 던진다.",
    desc: "동일한 요청을 그대로 다시 보낸다. 스트림이 시작된 뒤(이미 200을 받은 뒤) 중간에 끊기는 경우는 이 계층의 관할이 아니다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s 지수(최대 8s)",
    offHint: "-1 = 한 번도 재시도하지 않고, 실패하면 즉시 위로 던짐",
  },
  empty: {
    title: "빈 응답 재시도",
    where: "SDK · openai 형식만",
    trigger:
      "HTTP 200이고 finish_reason도 정상 stop인데 응답 전체에 콘텐츠 블록이 하나도 없음 —— 게이트웨이의 빈 프레임, 사고 필드 프레임 유실, 샘플링 오류가 이렇게 나타난다.",
    skips: "max_tokens 때문에 잘려서 내용이 없는 경우는 해당하지 않는다(출력 상한을 높여야 해결되며, 다시 보내면 또 같은 한계에 부딪힌다).",
    desc: "프롬프트 전체를 다시 보내므로 긴 컨텍스트에서는 비용이 크니, 횟수를 크게 잡지 않는 것이 좋다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s 지수(최대 8s)",
    offHint: "-1 = 빈 응답을 그대로 넘김",
  },
  stream: {
    title: "동일 provider 안전 구간 재시도",
    where: "이 프로젝트 · 출력이 전달되기 전",
    trigger:
      "스트림이 이미 수립된(200을 받은) 뒤에 문제가 발생: 연결이 중간에 끊김, 공급자 overloaded, 스트림 내 429 / 5xx 오류 이벤트 —— 그리고 토큰이 하나도 호출자에게 전달되지 않은 상태.",
    skips:
      "할당량 소진(402 / insufficient_quota, 라운드 로빈이 구성을 교체), 컨텍스트 초과(413 / context length, 압축으로 처리), 400 / 401 / 403 / 404 / 422 같은 확정적 거부는 모두 재시도하지 않는다.",
    desc: "동일한 구성에서 같은 요청을 다시 실행한다. 아직 어떤 출력도 전달되지 않았으므로 재실행해도 모델 출력이나 도구 실행이 중복되지 않는다.",
    attemptsLabel: "재시도 횟수",
    defAttempts: 2,
    defInterval: "0.5s→1s 지수(최대 4s)",
    offHint: "-1 = 스트림이 끊기면 바깥 계층의 의도 재실행으로 바로 넘김",
  },
  breaker: {
    title: "라운드 로빈 차단",
    where: "이 프로젝트 · 프로세스 단위, 전역에 하나",
    trigger:
      "일시적 실패(429, 5xx, 네트워크 오류)가 연속으로 임계값까지 누적되면 차단한다. 잔액 부족(402), 키 무효(401 / 403), 모델 없음(404) 같은 확정적 실패는 임계값과 무관하게 첫 번째에 바로 차단한다.",
    skips: "한 번 성공하면 초기화되므로, 가끔 오작동하는 구성이 조금씩 쌓여 차단되지는 않는다.",
    desc: "차단된 후에는 쿨다운에 들어가며, 쿨다운 동안 라운드 로빈은 이 구성을 바로 건너뛴다. 상태는 DB에 저장되어 재시작해도 사라지지 않는다.",
    attemptsLabel: "연속 몇 번 실패하면 차단할지",
    defAttempts: 3,
    defInterval: "1min→5min→30min 단계적",
    offHint: "-1 = 일시적 실패로는 절대 차단하지 않음(확정적 실패는 여전히 차단)",
  },
  intent: {
    title: "의도 재실행",
    where: "이 프로젝트 · 프로세스 단위, 전역에 하나",
    trigger:
      "앞의 계층들이 모두 막아내지 못함: worker가 model_error로 종료됨 —— 안쪽 재시도를 모두 소진했거나, 스트림이 출력을 전달하기 시작한 뒤에 끊김(이때는 재실행이 안전하지 않아 전체를 처음부터 다시 해야 함).",
    skips: "할당량 소진은 이미 라운드 로빈이 구성을 교체하여 처리하므로 여기서 재실행하지 않는다. 작업이 일시정지 / 종료 / 마무리 단계에 들어가면 즉시 양보하며 백오프 시간을 차지하지 않는다.",
    desc: "의도 전체를 처음부터 다시 실행한다. 가장 바깥 계층이므로 한 번의 재실행은 안쪽 계층들의 횟수가 다시 곱해진다는 뜻이다.",
    attemptsLabel: "재실행 횟수",
    defAttempts: 2,
    defInterval: "고정 3s",
    offHint: "-1 = 재실행하지 않으며, 해당 의도는 바로 blocked로 판정됨",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** 밀리초를 사람이 읽기 쉽게 표시한다. 입력란 옆에 되돌려 보여주는 용도로만 쓰며, 0을 세지 않아도 되게 한다. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** 제어형 숫자 입력: 빈 문자열 ↔ 0, 중간 상태("-", "1e")는 그대로 로컬에 두어 부모를 방해하지 않는다. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // 부모가 값 전체를 바꿨을 때(정책을 읽어 왔거나 구성을 전환했을 때) 따라간다. 직접 타이핑할 때는 여기에 오지 않는다.
  // 그때는 value가 이미 로컬 텍스트를 parse한 결과와 같기 때문이다.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** 한 계층 재시도의 두 노브. idPrefix는 같은 페이지에 여러 번 나타날 때 label의 htmlFor를 유지하는 데 쓴다. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = 구성 서랍의 간략 버전: 펼침 설명을 생략하고 「어떤 오류가 이 계층으로 오는지」 한 문장만 남긴다 */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* 어떤 오류가 이 계층으로 오는지 상태 코드까지 구체적으로 —— 노브를 채웠는데 효과가 안 보인다면 대부분 오류가 아예 이 계층에 해당하지 않는 것이다. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">트리거</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">이 계층으로 오지 않음</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`기본 ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            간격 ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="기본 백오프"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `고정 ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">비워 두면 기본값 사용; {meta.offHint}.</p>}
    </div>
  );
}

/** 모델 구성 서랍의 3개 계층 덮어쓰기(엔드포인트를 따르는 그 세 계층). */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">재시도 덮어쓰기</Label>
        <p className="text-muted-foreground text-xs">
          이 구성에만 적용되며, 「재시도 및 백오프」의 전역 기본값을 덮어씁니다. 각 칸을 비워 두면 전역을 따르고, 횟수에 -1을 입력하면 이 계층의 재시도가 꺼집니다.
          간격을 입력하면 지수 백오프 대신 고정 간격을 사용합니다. 차단과 의도 재실행은 프로세스 단위이므로 전역 페이지에서만 조정할 수 있습니다.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 「재시도 및 백오프」 tab: 5개 계층의 전역 기본값. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`재시도 정책을 읽지 못했습니다: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // 백엔드는 범위를 벗어난 값을 구간 안으로 되돌려 돌려주므로, 돌려받은 값으로 바로 새로고침하면 보이는 것이 곧 저장된 것이다.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("저장했으며 즉시 적용됩니다(현재 실행 중인 이번 호출은 이전 매개변수를 그대로 사용)");
    } catch (e) {
      toast.error(`저장 실패: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> 재시도 정책을 읽는 중…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        모델 호출 한 번의 실패는 안쪽에서 바깥쪽 순서로 5개 계층의 재시도를 차례로 거칩니다:
        <span className="text-foreground"> 연결 수립 → 빈 응답 → 동일 provider 안전 구간 → 라운드 로빈 차단 → 의도 재실행</span>
        . 안쪽을 모두 소진해야 바깥쪽 차례가 되므로 횟수는
        <span className="text-foreground">곱해집니다</span>
        —— 모든 계층을 최대로 올리면 한 번의 순간적 오류로 수십 건의 요청이 소모될 수 있습니다.
        모두 비워 두면 현재 기본값이며, 이 페이지가 없을 때의 동작과 완전히 같습니다. 앞의 세 계층은 각 모델 구성에서 개별적으로 덮어쓸 수 있습니다.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          저장
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          모두 기본값으로 복원
        </Button>
      </div>
    </div>
  );
}
