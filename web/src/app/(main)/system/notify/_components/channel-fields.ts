// 채널 필드 표와 설정 값 파싱 유틸리티.
//
// 페이지와 분리한 이유는 이 파일이 뷰가 아니라 **데이터**이기 때문이다: 각 채널에 어떤 필드가 있는지,
// 각각 어떤 컨트롤을 써야 하는지, 그리고 폼 텍스트와 설정 값(JSON) 사이의 양방향 변환을 기술한다.
// 별도 파일로 두면 새 채널을 추가할 때 여기만 고치면 되고 페이지 자체는 수정할 필요가 없다.
// 채널 유형의 표시 이름과 소개. 문구에만 영향을 주므로 프런트엔드에 두며, 백엔드는 알 필요가 없다.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "딩톡(DingTalk)",
  feishu: "페이슈(Feishu)",
  wecom: "WeCom(기업 위챗)",
  webhook: "범용 Webhook",
  telegram: "Telegram",
  email: "이메일",
};

// 각 채널의 설정 필드 정의.
//
// 백엔드가 schema를 내려주게 하지 않고 프런트엔드 필드 표를 의도적으로 따로 유지한다: 백엔드는
// Validate(필수/형식)만 담당하고 UI에 필요한 것은 레이아웃과 컨트롤 유형이므로, 둘은 관심사가 다르다.
// 唯一的耦合点是 secret_keys —— 哪些字段该渲染成密码框由后端给出，
// 어떤 값이 자격 증명인지는 채널 구현체만 알기 때문이다(WeCom은 Webhook 전체가 자격 증명이고,
// 而钉钉的只是其中一个 secret）。新增渠道时这里少一个条目只会让表单变空白，
// 조용히 오류가 나지는 않는다(아래 hasFields가 알려 준다).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "서명 키",
      kind: "password",
      help: "봇 보안 설정에서 「서명 추가」를 선택했을 때 입력하세요. 「사용자 정의 키워드」를 선택했거나 보안 설정을 켜지 않았다면 비워 두세요",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "서명 검증 키", kind: "password", help: "봇에서 「서명 검증」을 켰을 때 입력하고, 그렇지 않으면 비워 두세요" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook 주소",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "대상 URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "요청 메서드",
      kind: "select",
      options: [
        { value: "POST", label: "POST(요청 본문 포함)" },
        { value: "PUT", label: "PUT(요청 본문 포함)" },
        { value: "PATCH", label: "PATCH(요청 본문 포함)" },
        { value: "GET", label: "GET(요청 본문 없음)" },
      ],
    },
    { key: "headers", label: "사용자 정의 요청 헤더", kind: "kv", help: "한 줄에 하나씩 KEY=VALUE, 예: Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "요청 본문 템플릿",
      kind: "textarea",
      help:
        "비워 두면 내장 기본 템플릿을 사용합니다. 변수: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "그리고 range .Items 아래의 .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel. " +
        "문자열을 삽입할 때는 {{.Xxx}}가 아니라 {{json .Xxx}}를 사용하세요. 그렇지 않으면 제목 안의 따옴표가 JSON을 깨뜨립니다.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API 주소",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "비워 두면 공식 주소를 사용하고, 자체 구축한 Bot API 리버스 프록시를 쓸 때 입력하세요",
    },
  ],
  email: [
    { key: "host", label: "SMTP 서버", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "포트",
      kind: "number",
      placeholder: "587",
      help: "587은 STARTTLS를 사용하고, 465는 「암시적 TLS」를 켜세요",
    },
    { key: "username", label: "계정", kind: "text" },
    { key: "password", label: "비밀번호 / 인증 코드", kind: "password" },
    { key: "from", label: "발신자", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "수신자", kind: "list", help: "여러 주소는 쉼표로 구분하세요" },
    { key: "tls", label: "암시적 TLS", kind: "switch", help: "465 포트에서는 켜고, 587에서는 꺼 두세요(자동으로 STARTTLS 사용)" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "제한 없음" },
  { value: "low", label: "낮음 이상" },
  { value: "medium", label: "중간 이상" },
  { value: "high", label: "높음 이상" },
  { value: "critical", label: "치명만" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV는 「한 줄에 KEY=VALUE」 형식의 텍스트 영역을 파싱한다.
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs는 쉼표/공백으로 구분된 id 목록을 파싱한다.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords는 줄/쉼표로 구분된 키워드 목록을 파싱한다(취약점 유형 이름에 공백이 있을 수 있으므로 줄 또는 쉼표로 자른다).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,，]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
