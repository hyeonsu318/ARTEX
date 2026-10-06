"use client";

import { CheckIcon } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { NotificationFilter } from "@/lib/types";

// asText / inputType은 이 파일 내부의 값 변환 보조 함수이다(컨트롤 렌더링과 밀접하게 관련되므로 channel-fields에 두지 않는다).
import { type FieldDef, type FieldKind, SEVERITY_OPTIONS } from "./channel-fields";

// asText는 임의의 설정 값을 입력란에서 쓸 수 있는 문자열로 렌더링한다.
// config는 JSON에서 오므로 값이 string / number / boolean / array / null일 수 있다.
// 여기서는 「텍스트 상자에 넣을 수 있는가」만 신경 쓰며, 구체적인 직렬화는 buildConfig가 담당한다.
function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || v === undefined) return "";
  return String(v);
}

// inputType은 필드 타입을 input의 type 속성으로 매핑한다.
function inputType(kind: FieldKind): "text" | "password" | "number" {
  if (kind === "password") return "password";
  if (kind === "number") return "number";
  return "text";
}

// ConfigField는 필드 정의에 따라 해당 컨트롤을 렌더링한다.
//
// 마스킹된 필드 처리가 여기서 유일하게 신경 써야 할 부분이다: 입력란에는 마스킹 값 자체를 **표시하지 않고**,
// 「저장됨」이라는 안내 한 줄만 표시한다. 이렇게 하면 화면에는 규칙이 하나뿐이다 — 입력란에 글자가 있으면 사용자가 입력한 것이고,
// 빈 입력란이면 빈 값이다. "__masked__:…abc123"을 입력란에 넣으면 사용자는 그것이 직접
// 지워야 할 자리표시 텍스트라고 생각하여 오히려 자격 증명을 잘못 지우기 쉽다.
export function ConfigField({
  def,
  value,
  isSecret,
  onChange,
}: {
  def: FieldDef;
  value: unknown;
  isSecret: boolean;
  onChange: (v: unknown) => void;
}) {
  const id = `n-cfg-${def.key}`;
  const raw = asText(value);
  // 백엔드가 되돌려주는 마스킹 값: "__masked__:…abc123" 형태이며, 끝부분은 원래 값을 식별할 수 있는 조각이다.
  const masked = isSecret && raw.startsWith("__masked__");
  const maskedTail = masked ? (raw.split("…")[1] ?? "") : "";

  if (def.kind === "switch") {
    return (
      <div className="flex items-center gap-2 text-sm">
        <Switch checked={value === true} onCheckedChange={onChange} aria-label={def.label} />
        {def.label}
        {def.help && <span className="text-muted-foreground"> ({def.help})</span>}
      </div>
    );
  }

  if (def.kind === "select") {
    return (
      <div className="grid gap-2">
        <Label>{def.label}</Label>
        <Select value={raw || def.options?.[0]?.value} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.options ?? []).map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  // 컨트롤은 필드 타입에 따라 분기한다. 중첩 삼항 대신 if 체인을 쓰는 것은 여기서 네 가지 컨트롤을 구분해야 하는데,
  // 3중 삼항은 읽으려면 괄호를 세느라 멈춰야 하기 때문이다.
  function control() {
    if (def.kind === "textarea" || def.kind === "kv") {
      return (
        <Textarea
          id={id}
          className="font-mono"
          placeholder={def.placeholder}
          value={masked ? "" : raw}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    }
    if (def.kind === "list") {
      return (
        <Input
          id={id}
          value={Array.isArray(value) ? (value as string[]).join(", ") : raw}
          onChange={(e) => onChange(e.target.value)}
          placeholder={def.placeholder}
        />
      );
    }
    return (
      <Input
        id={id}
        className={def.kind === "text" ? "font-mono" : ""}
        type={inputType(def.kind)}
        placeholder={def.placeholder}
        value={masked ? "" : raw}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  const hint = masked ? (
    <p className="text-muted-foreground flex items-center gap-1 text-xs">
      <CheckIcon className="size-3" />
      저장됨{maskedTail ? ` (끝자리 ${maskedTail})` : ""} · 새 값을 입력하면 덮어쓰고, 비우면 해당 항목이 삭제됩니다
    </p>
  ) : (
    def.help && <p className="text-muted-foreground text-xs">{def.help}</p>
  );

  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{def.label}</Label>
      {control()}
      {hint}
    </div>
  );
}

// FilterSummary는 필터 조건을 한 줄로 요약하여, 카드를 펼치지 않아도 이 채널이 무엇을 푸시하는지 알 수 있게 한다.
export function FilterSummary({ filter }: { filter: NotificationFilter }) {
  const parts: string[] = [];
  if (filter.min_severity) {
    parts.push(SEVERITY_OPTIONS.find((o) => o.value === filter.min_severity)?.label ?? filter.min_severity);
  }
  if (filter.vulnclass_include?.length) parts.push(`유형 포함 ${filter.vulnclass_include.length}개 단어`);
  if (filter.vulnclass_exclude?.length) parts.push(`제외 ${filter.vulnclass_exclude.length}개 단어`);
  if (filter.task_ids?.length) parts.push(`작업 ${filter.task_ids.length}개`);
  if (filter.asset_ids?.length) parts.push(`자산 ${filter.asset_ids.length}개`);
  if (filter.on_status_change) parts.push("상태 변경 포함");
  if (parts.length === 0) {
    return <p className="text-muted-foreground text-sm">전체 취약점</p>;
  }
  return <p className="text-muted-foreground text-sm">{parts.join(" · ")}</p>;
}
