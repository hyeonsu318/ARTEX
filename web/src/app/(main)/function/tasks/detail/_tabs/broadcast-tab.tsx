"use client";

import * as React from "react";

import {
  ArrowDownIcon,
  ArrowUpIcon,
  ArrowUpToLineIcon,
  BugIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CompassIcon,
  FlagIcon,
  FlaskConicalIcon,
  LayersIcon,
  LightbulbIcon,
  type LucideIcon,
  PauseIcon,
  PlayIcon,
  SearchIcon,
  TargetIcon,
} from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import { type Tone, toneClasses, toneDot } from "@/lib/status";
import { taskAssetTypeLabel } from "@/lib/task-assets";
import type { Edge, ExploreKind, FindingAsset, NewAssetType, TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZES = [20, 50, 100];
const POLL_MS = 8000;

type KindMeta = { label: string; icon: LucideIcon; dot: string; chip: string };

// 브로드캐스트 보드 전용 표시 메타데이터. 탐색 경로 그래프의 것을 일부러 재사용하지 않는다: 그래프는 토폴로지 관점(노드 카드, 연결선 색상)이고,
// 브로드캐스트는 흐름 관점(타임라인 행)이라 두 쪽의 정보 밀도와 색상 요구가 다르므로, 각자 발전시키는 편이 더 쉽다.
const KIND_META: Record<string, KindMeta> = {
  begin: {
    label: "시작점",
    icon: FlagIcon,
    dot: "bg-slate-500",
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
  },
  task: {
    label: "루트 작업",
    icon: FlagIcon,
    dot: "bg-slate-500",
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
  },
  goal: {
    label: "목표",
    icon: TargetIcon,
    dot: "bg-emerald-500",
    chip: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  },
  intent: {
    label: "의도",
    icon: CompassIcon,
    dot: "bg-blue-500",
    chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
  },
  fact: {
    label: "사실",
    icon: FlaskConicalIcon,
    dot: "bg-amber-500",
    chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
  },
  finding: {
    label: "취약점",
    icon: BugIcon,
    dot: "bg-rose-500",
    chip: "bg-rose-500/15 text-rose-600 dark:text-rose-400",
  },
  hint: {
    label: "힌트",
    icon: LightbulbIcon,
    dot: "bg-violet-500",
    chip: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  },
  digest: {
    label: "압축",
    icon: LayersIcon,
    dot: "bg-teal-500",
    chip: "bg-teal-500/15 text-teal-600 dark:text-teal-400",
  },
};

// 필터링 가능한 유형. 시작점(fact/state=origin)은 따로 나열하지 않고 「사실」과 함께 필터링된다.
const FILTER_KINDS: ExploreKind[] = ["goal", "intent", "fact", "finding", "hint", "digest"];

const REL_LABEL: Record<string, string> = {
  spawns: "파생",
  derived_from: "의도 체인",
  yields: "산출",
  proves: "증명",
  covers: "압축",
};

// goal / intent의 상태 의미는 전역 status 표(StatusBadge)가 제공하며, 나머지 유형의 상태는 그래프와
// 브로드캐스트에서만 나타나므로 여기에 따로 둔다.
const STATE_META: Record<string, Record<string, { label: string; tone: Tone }>> = {
  fact: {
    origin: { label: "시작점", tone: "slate" },
    confirmed: { label: "확인됨", tone: "green" },
    dismissed: { label: "부정됨", tone: "slate" },
  },
  finding: {
    confirmed: { label: "확인됨", tone: "red" },
    dismissed: { label: "배제됨", tone: "slate" },
  },
  hint: {
    active: { label: "채택 대기", tone: "violet" },
    consumed: { label: "채택됨", tone: "slate" },
  },
  digest: {
    active: { label: "유효", tone: "green" },
    superseded: { label: "대체됨", tone: "slate" },
  },
};

// 작업 루트는 state=origin인 fact이며, 브로드캐스트에서는 「시작점」으로 읽힌다.
function viewKind(n: TaskNode): string {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

const SUMMARY_FIELDS: Record<string, string[]> = {
  begin: ["summary", "description"],
  task: ["summary", "description"],
  goal: ["text"],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["name", "summary"],
  hint: ["text", "summary"],
  digest: ["body", "summary"],
};

function summaryOf(n: TaskNode): string {
  const raw = n.payload ?? "";
  if (!raw.trim()) return "";
  for (const field of SUMMARY_FIELDS[viewKind(n)] ?? []) {
    try {
      const obj: unknown = JSON.parse(raw);
      if (obj && typeof obj === "object") {
        const v = (obj as Record<string, unknown>)[field];
        if (typeof v === "string" && v.trim()) return v;
      }
    } catch {
      return raw; // JSON이 아닌 payload: 그대로 브로드캐스트
    }
  }
  return raw;
}

function prettyPayload(raw?: string): string {
  if (!raw?.trim()) return "(payload 없음)";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function relTime(ts: number, now: number): string {
  if (!now || !ts) return "";
  const sec = Math.max(0, (now - ts) / 1000);
  if (sec < 60) return "방금";
  if (sec < 3600) return `${Math.floor(sec / 60)}분 전`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}시간 전`;
  return `${Math.floor(sec / 86400)}일 전`;
}

const dayFmt = new Intl.DateTimeFormat("zh-CN", { month: "long", day: "numeric", weekday: "short" });
const clockFmt = new Intl.DateTimeFormat("zh-CN", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

function NodeStateBadge({ node }: { node: TaskNode }) {
  if (node.type === "goal") return <StatusBadge domain="goal" value={node.state} dot />;
  if (node.type === "intent") return <StatusBadge domain="intent" value={node.state} dot />;
  const meta = STATE_META[node.type]?.[node.state];
  if (!meta) return null;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        toneClasses[meta.tone],
      )}
    >
      <span className={cn("size-1.5 rounded-full", toneDot[meta.tone])} />
      {meta.label}
    </span>
  );
}

function KindChip({ kind }: { kind: string }) {
  const meta = KIND_META[kind] ?? KIND_META.fact;
  return (
    <span className={cn("rounded px-1.5 py-0.5 text-xs font-medium whitespace-nowrap", meta.chip)}>{meta.label}</span>
  );
}

// 노드에 고정된 자산: 유형 라벨 + 식별 가능한 텍스트. 데이터는 브로드캐스트 페이지와 함께 내려오고(node id → 자산),
// 펼칠 때 바로 표시하며 추가 요청을 하지 않는다.
function AssetList({ assets, dense = false }: { assets: FindingAsset[]; dense?: boolean }) {
  if (assets.length === 0) return null;
  return (
    <div>
      <div className="mb-1.5 text-xs font-medium text-muted-foreground">관련 자산 · {assets.length}</div>
      <ul className="flex flex-wrap gap-1.5">
        {assets.map((a) => {
          // 런타임에 a.type이 라벨 표가 다루지 않는 유형일 수 있으므로 원본 문자열로 되돌린다. 타입을 한 번 변환해 두어 대체 처리가 불필요한 것으로 판정되지 않게 한다.
          const typeLabel =
            (taskAssetTypeLabel as (t: NewAssetType) => string | undefined)(a.type as NewAssetType) || a.type;
          return (
            <li
              key={a.id}
              className={cn(
                "inline-flex max-w-full items-center gap-1.5 rounded-md border bg-background px-2 py-0.5",
                dense && "text-xs",
              )}
              title={`${typeLabel} · ${a.label}`}
            >
              <span className="shrink-0 rounded bg-muted px-1 text-[10px] text-muted-foreground">{typeLabel}</span>
              <code className="truncate font-mono text-xs">{a.label}</code>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// 상·하류 항목에 마우스를 올릴 때 뜨는 노드 카드: 유형/상태/출처/시간 + 요약 + payload 조각 + 관련 자산.
// 데이터는 이 페이지가 이미 가져온 refs에서 오며 추가 요청을 보내지 않는다 —— 브로드캐스트 API가 이웃 노드를 통째로 가져다 주었기 때문이다.
function RelatedNodeCard({ node, assets }: { node: TaskNode; assets: FindingAsset[] }) {
  const kind = viewKind(node);
  const meta = KIND_META[kind] ?? KIND_META.fact;
  const ts = Date.parse(node.ts);
  const summary = summaryOf(node);
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <KindChip kind={kind} />
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">#{node.id}</code>
        <NodeStateBadge node={node} />
        {node.priority > 0 && (kind === "goal" || kind === "intent") && (
          <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">P{node.priority}</span>
        )}
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
        <span>유형 {meta.label}</span>
        <span>출처 {node.origin || "system"}</span>
        <span>{Number.isNaN(ts) ? node.ts : new Date(ts).toLocaleString("zh-CN")}</span>
      </div>
      <p className="line-clamp-4 text-xs break-words">{summary || "(요약 없음)"}</p>
      <AssetList assets={assets} dense />
      <pre className="max-h-40 overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap">
        {prettyPayload(node.payload)}
      </pre>
    </div>
  );
}

// 한 브로드캐스트와 관련된 상·하류: 상류 = 이 노드를 가리키는 엣지, 하류 = 이 노드가 가리키는 엣지.
function RelatedList({
  title,
  rows,
  refs,
  assets,
}: {
  title: string;
  rows: Array<{ rel: string; id: string }>;
  refs: Record<string, TaskNode>;
  assets: Record<string, FindingAsset[]>;
}) {
  if (rows.length === 0) return null;
  return (
    <div className="min-w-0 flex-1">
      <div className="mb-1.5 text-xs font-medium text-muted-foreground">{title}</div>
      <ul className="flex flex-col gap-1.5">
        {rows.map((row) => {
          const node = refs[row.id];
          return (
            <li key={`${row.rel}-${row.id}`} className="flex min-w-0 items-center gap-2 text-xs">
              <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-muted-foreground">
                {REL_LABEL[row.rel] ?? row.rel}
              </span>
              {node ? (
                <HoverCard openDelay={150} closeDelay={100}>
                  <HoverCardTrigger asChild>
                    <button
                      type="button"
                      className="flex min-w-0 cursor-help items-center gap-2 text-left hover:underline"
                    >
                      <KindChip kind={viewKind(node)} />
                      <span className="truncate">{summaryOf(node) || `노드 #${node.id}`}</span>
                    </button>
                  </HoverCardTrigger>
                  <HoverCardContent align="start" className="w-96">
                    <RelatedNodeCard node={node} assets={assets[node.id] ?? []} />
                  </HoverCardContent>
                </HoverCard>
              ) : (
                <span className="text-muted-foreground">노드 #{row.id}</span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function BroadcastRow({
  node,
  edges,
  refs,
  assets,
  now,
  fresh,
}: {
  node: TaskNode;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  assets: Record<string, FindingAsset[]>;
  now: number;
  fresh: boolean;
}) {
  const [open, setOpen] = React.useState(false);
  const kind = viewKind(node);
  const meta = KIND_META[kind] ?? KIND_META.fact;
  const Icon = meta.icon;
  const ts = Date.parse(node.ts);
  const summary = summaryOf(node);
  const upstream = edges.filter((e) => e.dst === node.id).map((e) => ({ rel: e.rel, id: e.src }));
  const downstream = edges.filter((e) => e.src === node.id).map((e) => ({ rel: e.rel, id: e.dst }));

  return (
    <div className={cn("relative grid grid-cols-[4.5rem_1.75rem_1fr] gap-x-2", fresh && "bg-primary/5")}>
      {/* 시간 열 */}
      <div className="py-3 text-right text-xs text-muted-foreground tabular-nums">
        <div>{Number.isNaN(ts) ? "--:--:--" : clockFmt.format(ts)}</div>
        <div className="text-[11px] opacity-70">{relTime(ts, now)}</div>
      </div>

      {/* 타임라인: 세로선 + 유형 점 */}
      <div className="relative flex justify-center">
        <span className="absolute inset-y-0 w-px bg-border" />
        <span
          className={cn(
            "relative mt-3.5 flex size-6 items-center justify-center rounded-full text-white ring-4 ring-background",
            meta.dot,
          )}
        >
          <Icon className="size-3.5" />
        </span>
      </div>

      {/* 내용 열 */}
      <div className="min-w-0 border-b py-3 pr-1 last:border-b-0">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className="flex w-full min-w-0 items-start gap-2 text-left"
        >
          <ChevronRightIcon
            className={cn("mt-0.5 size-3.5 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
          />
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 flex-wrap items-center gap-1.5">
              <KindChip kind={kind} />
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">#{node.id}</code>
              <NodeStateBadge node={node} />
              {node.priority > 0 && (kind === "goal" || kind === "intent") && (
                <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">P{node.priority}</span>
              )}
              {fresh && (
                <span className="rounded bg-primary px-1.5 py-0.5 text-[10px] font-semibold text-primary-foreground">
                  신규
                </span>
              )}
              <span className="ml-auto shrink-0 text-xs text-muted-foreground">{node.origin || "system"}</span>
            </div>
            <p className={cn("mt-1 text-sm", !open && "line-clamp-2")}>{summary || `노드 #${node.id}`}</p>
          </div>
        </button>

        {open && (
          <div className="mt-2 ml-5 flex flex-col gap-3 rounded-md border bg-muted/30 p-3">
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span>
                노드 <code className="font-mono">#{node.id}</code>
              </span>
              <span>유형 {meta.label}</span>
              <span>출처 {node.origin || "system"}</span>
              <span>{Number.isNaN(ts) ? node.ts : new Date(ts).toLocaleString("zh-CN")}</span>
            </div>
            {node.state === "deleted" && node.delete_reason && (
              <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs">
                <span className="font-medium text-destructive">삭제 사유</span>
                <span className="ml-2 break-words text-muted-foreground">{node.delete_reason}</span>
              </div>
            )}
            <AssetList assets={assets[node.id] ?? []} />
            {(upstream.length > 0 || downstream.length > 0) && (
              <div className="flex flex-col gap-3 sm:flex-row">
                <RelatedList title="상류 · 여기서 비롯됨" rows={upstream} refs={refs} assets={assets} />
                <RelatedList title="하류 · 여기서 생성됨" rows={downstream} refs={refs} assets={assets} />
              </div>
            )}
            <div>
              <div className="mb-1.5 text-xs font-medium text-muted-foreground">payload</div>
              <pre className="max-h-64 overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap">
                {prettyPayload(node.payload)}
              </pre>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function BroadcastTab({ taskId }: { taskId: string }) {
  const [kinds, setKinds] = React.useState<ExploreKind[]>([]);
  const [queryInput, setQueryInput] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [order, setOrder] = React.useState<"asc" | "desc">("desc");
  const [page, setPage] = React.useState(1);
  const [size, setSize] = React.useState(20);
  const [live, setLive] = React.useState(true);

  const [items, setItems] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const [refs, setRefs] = React.useState<Record<string, TaskNode>>({});
  const [assets, setAssets] = React.useState<Record<string, FindingAsset[]>>({});
  const [total, setTotal] = React.useState(0);
  const [loaded, setLoaded] = React.useState(false);
  const [freshIDs, setFreshIDs] = React.useState<Set<string>>(new Set());
  const [pending, setPending] = React.useState(0);
  const [now, setNow] = React.useState(0);

  const seenRef = React.useRef<Set<string>>(new Set());
  const baselineRef = React.useRef<number | null>(null);
  const streamRef = React.useRef("");
  // 「최신 순 1페이지」만이 진짜 라이브 위치이며, 나머지 위치에서는 폴링이 읽지 않은 개수만 갱신하고
  // 목록은 건드리지 않아, 페이지를 넘기거나 펼칠 때 내용이 발밑에서 바뀌지 않게 한다.
  const atLive = page === 1 && order === "desc";

  React.useEffect(() => {
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(t);
  }, []);

  // 입력 디바운스: 타이핑을 300ms 멈춰야 실제로 조회하고 첫 페이지로 돌아간다.
  React.useEffect(() => {
    const t = setTimeout(() => {
      setQuery(queryInput);
      setPage(1);
    }, 300);
    return () => clearTimeout(t);
  }, [queryInput]);

  React.useEffect(() => {
    let alive = true;
    let rendered = false; // 이번 조회에서 내용을 이미 렌더링했는지 여부
    // 작업/필터/정렬 변경 = 다른 브로드캐스트 스트림으로 바뀜: 「신규」 표시와 읽지 않음 기준선을 지운다. 페이지 넘김은 스트림 변경이 아니며,
    // 그렇지 않으면 최신으로 돌아왔을 때 계산할 읽지 않음 개수가 없어진다.
    const stream = `${taskId}|${kinds.join(",")}|${query}|${order}`;
    if (streamRef.current !== stream) {
      streamRef.current = stream;
      seenRef.current = new Set();
      baselineRef.current = null;
      setPending(0);
    }
    const load = () =>
      api
        .explorationNodes(taskId, { page, size, kinds, q: query, order })
        .then((r) => {
          if (!alive) return;
          // 라이브 위치는 매번 새로고침하고, 다른 위치는 처음 한 번만 렌더링하며 이후 폴링은 읽지 않은 개수만 갱신한다.
          if (atLive || !rendered) {
            rendered = true;
            setItems(r.items);
            setEdges(r.edges);
            setRefs(r.refs);
            setAssets(r.assets);
          }
          setTotal(r.total);
          if (atLive) {
            const seen = seenRef.current;
            setFreshIDs(seen.size === 0 ? new Set() : new Set(r.items.filter((n) => !seen.has(n.id)).map((n) => n.id)));
            seenRef.current = new Set(r.items.map((n) => n.id));
            baselineRef.current = r.total;
            setPending(0);
          } else {
            const base = baselineRef.current;
            setPending(base === null ? 0 : Math.max(0, r.total - base));
          }
          setLoaded(true);
        })
        .catch(() => {
          // 폴링은 최선 노력 방식이다: 마지막으로 성공한 브로드캐스트 내용을 유지하고 다음 라운드에 자동으로 재시도한다.
        });
    void load();
    if (!live) {
      return () => {
        alive = false;
      };
    }
    const timer = setInterval(load, POLL_MS);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [taskId, page, size, kinds, query, order, live, atLive]);

  const toggleKind = (kind: ExploreKind) => {
    setKinds((cur) => (cur.includes(kind) ? cur.filter((k) => k !== kind) : [...cur, kind]));
    setPage(1);
  };

  const backToLive = () => {
    setPage(1);
    setOrder("desc");
    setPending(0);
  };

  // 서버의 refs는 「이 페이지에 없는 이웃」만 보충하므로, 같은 페이지 노드 간의 참조는 items 자체로 보완해야 한다.
  // 그렇지 않으면 인접한 두 브로드캐스트가 서로를 참조할 때 맨 「노드 #id」로 퇴화한다.
  const nodeIndex = React.useMemo(() => {
    const idx: Record<string, TaskNode> = { ...refs };
    for (const n of items) idx[n.id] = n;
    return idx;
  }, [refs, items]);

  const pageCount = Math.max(1, Math.ceil(total / size));
  const start = total === 0 ? 0 : (page - 1) * size + 1;
  const end = (page - 1) * size + items.length;

  // 작업이나 필터를 바꿔 건수가 줄어들면 범위를 벗어난 페이지 번호를 되돌린다.
  React.useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);

  // 날짜별 그룹: 브로드캐스트 스트림을 날짜로 줄바꿈하므로, 긴 작업에서 페이지를 넘겨도 「이게 어느 날의 일인지」 알아볼 수 있다.
  const groups: Array<{ day: string; rows: TaskNode[] }> = [];
  for (const node of items) {
    const ts = Date.parse(node.ts);
    const day = Number.isNaN(ts) ? "알 수 없는 날짜" : dayFmt.format(ts);
    const last = groups[groups.length - 1];
    if (last && last.day === day) last.rows.push(node);
    else groups.push({ day, rows: [node] });
  }

  return (
    <Card className="overflow-hidden py-0">
      {/* 툴바 */}
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-2.5">
        <div className="relative w-full sm:w-64">
          <SearchIcon className="absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={queryInput}
            onChange={(e) => setQueryInput(e.target.value)}
            placeholder="내용 / 출처 / 노드 id 검색"
            className="h-8 pl-8"
            aria-label="브로드캐스트 검색"
          />
        </div>
        <div className="flex flex-wrap items-center gap-1">
          {FILTER_KINDS.map((kind) => {
            const meta = KIND_META[kind];
            const active = kinds.includes(kind);
            return (
              <button
                key={kind}
                type="button"
                onClick={() => toggleKind(kind)}
                aria-pressed={active}
                className={cn(
                  "rounded-md border px-2 py-0.5 text-xs font-medium transition-colors",
                  active ? meta.chip : "border-transparent text-muted-foreground hover:bg-accent",
                )}
              >
                {meta.label}
              </button>
            );
          })}
          {kinds.length > 0 && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => {
                setKinds([]);
                setPage(1);
              }}
            >
              지우기
            </Button>
          )}
        </div>
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            className="h-8"
            onClick={() => {
              setOrder((o) => (o === "desc" ? "asc" : "desc"));
              setPage(1);
            }}
            aria-label={order === "desc" ? "현재 최신 순, 클릭하면 오래된 순으로 변경" : "현재 오래된 순, 클릭하면 최신 순으로 변경"}
          >
            {order === "desc" ? <ArrowDownIcon /> : <ArrowUpIcon />}
            {order === "desc" ? "최신 순" : "오래된 순"}
          </Button>
          <Button
            variant={live ? "outline" : "secondary"}
            size="sm"
            className="h-8"
            onClick={() => setLive((v) => !v)}
            aria-label={live ? "자동 새로고침 일시정지" : "자동 새로고침 재개"}
          >
            {live ? <PauseIcon /> : <PlayIcon />}
            {live ? "자동 새로고침" : "일시정지됨"}
          </Button>
        </div>
      </div>

      {/* 라이브 위치를 벗어났을 때의 읽지 않음 알림 */}
      {!atLive && pending > 0 && (
        <button
          type="button"
          onClick={backToLive}
          className="flex w-full items-center justify-center gap-1.5 border-b bg-primary/10 py-1.5 text-xs font-medium text-primary hover:bg-primary/15"
        >
          <ArrowUpToLineIcon className="size-3.5" />
          {pending > 99 ? "99+" : pending} 건의 새 브로드캐스트 · 최신으로 돌아가기
        </button>
      )}

      <CardContent className="px-4 py-0">
        {!loaded ? (
          <div className="flex flex-col gap-3 py-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </div>
        ) : items.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">
            {query || kinds.length > 0 ? "조건에 맞는 브로드캐스트가 없습니다." : "이 작업은 아직 탐색 노드를 만들지 않았습니다."}
          </p>
        ) : (
          groups.map((group) => (
            <div key={group.day}>
              <div className="py-2 pl-[6.25rem] text-xs font-medium text-muted-foreground">{group.day}</div>
              {group.rows.map((node) => (
                <BroadcastRow
                  key={node.id}
                  node={node}
                  edges={edges}
                  refs={nodeIndex}
                  assets={assets}
                  now={now}
                  fresh={freshIDs.has(node.id)}
                />
              ))}
            </div>
          ))
        )}
      </CardContent>

      <CardFooter className="flex flex-wrap items-center gap-2 border-t px-4 py-2.5 text-xs text-muted-foreground">
        <Select
          value={String(size)}
          onValueChange={(v) => {
            setSize(Number(v));
            setPage(1);
          }}
        >
          <SelectTrigger size="sm" className="h-7 w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {PAGE_SIZES.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  {n} / 페이지
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <span className="tabular-nums">
          {start}–{end} / {total}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="outline"
            size="icon-sm"
            disabled={page <= 1}
            onClick={() => setPage((p) => Math.max(1, p - 1))}
            aria-label="이전 페이지"
          >
            <ChevronLeftIcon />
          </Button>
          <span className="tabular-nums">
            {page} / {pageCount}
          </span>
          <Button
            variant="outline"
            size="icon-sm"
            disabled={page >= pageCount}
            onClick={() => setPage((p) => Math.min(pageCount, p + 1))}
            aria-label="다음 페이지"
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </CardFooter>
    </Card>
  );
}
