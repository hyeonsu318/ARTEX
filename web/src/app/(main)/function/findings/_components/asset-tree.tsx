"use client";

import * as React from "react";

import {
  BuildingIcon,
  ChevronRightIcon,
  CircleDashedIcon,
  GlobeIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  RefreshCwIcon,
  SearchIcon,
  SmartphoneIcon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import type { FindingAssetKind, FindingAssetNode } from "@/lib/types";
import { cn } from "@/lib/utils";

// 아이콘은 자산 페이지의 유형 매핑을 그대로 사용하여, 같은 종류의 자산이 두 곳에서 똑같이 보이게 한다.
const KIND_ICON: Record<FindingAssetKind, LucideIcon> = {
  company: BuildingIcon,
  root_domain: GlobeIcon,
  subdomain: GlobeIcon,
  ip: NetworkIcon,
  app: SmartphoneIcon,
  service: LayoutTemplateIcon,
  endpoint: LinkIcon,
  none: CircleDashedIcon,
};

const KIND_LABEL: Record<FindingAssetKind, string> = {
  company: "기업",
  root_domain: "루트 도메인",
  subdomain: "서브도메인",
  ip: "IP",
  app: "앱",
  service: "서비스",
  endpoint: "엔드포인트",
  none: "연결 안 됨",
};

// TreeNode는 노드 배열로 조립한 트리이다. 백엔드가 이미 「같은 부모 아래에서 발견이 많은 것이 앞」이 되도록 정렬해 두었으므로,
// 여기서는 배열 순서대로 붙이기만 하면 된다.
interface TreeNode extends FindingAssetNode {
  children: TreeNode[];
  depth: number;
  /** 트리에 실제로 렌더링되는 글자; 전체 label은 label에 그대로 보존된다(호버 툴팁과 브레드크럼에서 사용). */
  display: string;
}

function stripBrackets(host: string) {
  return host.startsWith("[") && host.endsWith("]") ? host.slice(1, -1) : host;
}

function parseAssetURL(raw: string): URL | null {
  try {
    return new URL(raw);
  } catch {
    return null;
  }
}

// hostOf는 노드가 대표하는 호스트를 가져온다: URL은 hostname, 「host:port」는 host, 나머지는
// 라벨 자체(루트 도메인 / 서브도메인 / IP)이다.
function hostOf(label: string): string {
  const url = parseAssetURL(label);
  if (url) return stripBrackets(url.hostname);
  const hostPort = label.match(/^(.+):(\d+)$/);
  return stripBrackets(hostPort ? hostPort[1] : label);
}

// shortLabel은 부모 노드와 중복되는 접두사를 제거한다. service / endpoint의 label은 전체 URL인데,
// 호스트 도메인/IP는 바로 윗줄에 이미 적혀 있다 —— 깊은 노드는 원래 폭이 좁은데 host를 또 반복하면, 실제로
// 정보량이 있는 포트와 경로가 모두 잘려 나간다. 전체 값은 여전히 title과 브레드크럼에 있다.
function shortLabel(node: FindingAssetNode, parent?: FindingAssetNode): string {
  if (!parent) return node.label;

  // 서브도메인은 루트 도메인 아래에 붙는다: 루트 도메인 접미사를 제거하고 자기 부분만 남긴다.
  if (node.kind === "subdomain" && node.label.endsWith(`.${parent.label}`)) {
    return node.label.slice(0, -(parent.label.length + 1)) || node.label;
  }
  if (node.kind !== "service" && node.kind !== "endpoint") return node.label;

  // 부모 라벨이 정확히 자신의 접두사인 경우(엔드포인트가 같은 URL의 서비스 아래, 서비스가 같은 IP 아래): 그냥 잘라낸다.
  if (node.label.startsWith(parent.label)) {
    return node.label.slice(parent.label.length) || node.label;
  }

  // 그렇지 않으면 부모 노드가 실제로 이 URL의 호스트일 때만 축약하고, 아니면 식별 정보가 사라진다
  // (예: 서비스가 서브도메인 자산 행이 없어서 루트 도메인 바로 아래에 붙은 경우에는 전체 URL을 표시해야 한다).
  if (hostOf(node.label) !== hostOf(parent.label)) return node.label;

  const url = parseAssetURL(node.label);
  if (!url) return node.label;
  if (node.kind === "endpoint") return `${url.pathname}${url.search}` || "/";
  const scheme = url.protocol.replace(":", "");
  const port = url.port || (url.protocol === "https:" ? "443" : "80");
  return `${scheme} :${port}`;
}

export function buildAssetTree(nodes: FindingAssetNode[]): TreeNode[] {
  const byKey = new Map<string, TreeNode>();
  for (const node of nodes) {
    byKey.set(node.key, { ...node, children: [], depth: 0, display: node.label });
  }
  const roots: TreeNode[] = [];
  for (const node of nodes) {
    const current = byKey.get(node.key);
    if (!current) continue;
    const parent = node.parent ? byKey.get(node.parent) : undefined;
    // 부모 노드가 없으면(계층이 잘려 나가 사라진 경우) 최상위로 끌어올려, 하위 트리 전체가 사라지지 않게 한다.
    if (parent) {
      parent.children.push(current);
      current.display = shortLabel(node, parent);
    } else {
      roots.push(current);
    }
  }
  const setDepth = (node: TreeNode, depth: number) => {
    node.depth = depth;
    for (const child of node.children) setDepth(child, depth + 1);
  };
  for (const root of roots) setDepth(root, 0);
  return roots;
}

// assetPathOf는 최상위에서 해당 노드까지의 경로를 반환하며, 오른쪽 브레드크럼에 사용된다. 각 단계 역시 상대적으로
// 윗단계 대비 증분(display)만 표시하고, 전체 값은 label에 남겨 둔다.
export function assetPathOf(nodes: FindingAssetNode[], key: string | null): (FindingAssetNode & { display: string })[] {
  if (!key) return [];
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const path: FindingAssetNode[] = [];
  const seen = new Set<string>();
  let current = byKey.get(key);
  while (current && !seen.has(current.key)) {
    seen.add(current.key);
    path.unshift(current);
    current = current.parent ? byKey.get(current.parent) : undefined;
  }
  return path.map((node, index) => ({ ...node, display: shortLabel(node, path[index - 1]) }));
}

// filterTree는 키워드로 필터링한다: 일치한 노드를 유지하고 그 조상 체인 전체도 유지한다(조상 자체는 일치하지 않아도 된다).
// 일치한 노드의 자손도 함께 유지하므로 계속 아래로 파고들 수 있다.
function filterTree(nodes: TreeNode[], keyword: string): TreeNode[] {
  const kw = keyword.trim().toLowerCase();
  if (!kw) return nodes;
  const walk = (node: TreeNode): TreeNode | null => {
    const hit = node.label.toLowerCase().includes(kw);
    if (hit) return node;
    const children = node.children.map(walk).filter((c): c is TreeNode => c !== null);
    if (children.length === 0) return null;
    return { ...node, children };
  };
  return nodes.map(walk).filter((n): n is TreeNode => n !== null);
}

// collectKeys는 (하위) 트리 하나의 모든 key를 수집하며, 「일치 항목 모두 펼치기」에 사용된다.
function collectKeys(nodes: TreeNode[], out: Set<string> = new Set()): Set<string> {
  for (const node of nodes) {
    out.add(node.key);
    collectKeys(node.children, out);
  }
  return out;
}

interface AssetTreeProps {
  nodes: FindingAssetNode[];
  selected: string | null;
  onSelect: (key: string | null) => void;
  loading?: boolean;
  truncated?: boolean;
  droppedKinds?: string[];
  /** 어떤 자산도 선택하지 않았을 때 오른쪽에 표시되는 발견 총수로, 「전체 자산」 행에 사용된다. */
  findingTotal: number;
  /** 자산 보기는 폴링하지 않으므로, 트리의 카운트는 이 버튼이나 페이지 내의 추가·삭제·수정으로 갱신된다. */
  onRefresh?: () => void;
}

export function AssetTree({
  nodes,
  selected,
  onSelect,
  loading,
  truncated,
  droppedKinds,
  findingTotal,
  onRefresh,
}: AssetTreeProps) {
  const [keyword, setKeyword] = React.useState("");
  const [expanded, setExpanded] = React.useState<Set<string>>(() => new Set());
  // 사용자가 수동으로 접은 노드를 기억하여, 「기본으로 최상위 펼침」이 새로고침할 때마다 다시 펼쳐 놓지 않게 한다.
  const [collapsed, setCollapsed] = React.useState<Set<string>>(() => new Set());

  const roots = React.useMemo(() => buildAssetTree(nodes), [nodes]);
  const visible = React.useMemo(() => filterTree(roots, keyword), [roots, keyword]);

  // 검색할 때는 일치한 가지를 모두 펼친다. 그렇지 않으면 일치 항목이 접힌 노드 안에 숨어 검색하지 않은 것과 같다.
  const searching = keyword.trim() !== "";
  const searchKeys = React.useMemo(() => (searching ? collectKeys(visible) : null), [searching, visible]);

  const isExpanded = React.useCallback(
    (node: TreeNode) => {
      if (searchKeys) return searchKeys.has(node.key);
      if (expanded.has(node.key)) return true;
      // 최상위는 기본으로 한 단계 펼친다: 더 깊은 계층은 사용자가 직접 열도록 하여, 한 번에 수천 행이 펼쳐지는 것을 피한다.
      return node.depth === 0 && !collapsed.has(node.key);
    },
    [collapsed, expanded, searchKeys],
  );

  const toggle = React.useCallback(
    (node: TreeNode) => {
      const open = isExpanded(node);
      setExpanded((prev) => {
        const next = new Set(prev);
        if (open) next.delete(node.key);
        else next.add(node.key);
        return next;
      });
      setCollapsed((prev) => {
        const next = new Set(prev);
        if (open) next.add(node.key);
        else next.delete(node.key);
        return next;
      });
    },
    [isExpanded],
  );

  let emptyHint = "현재 필터에서 자산과 연결된 발견 사항이 없습니다.";
  if (loading) emptyHint = "로딩 중…";
  else if (searching) emptyHint = "일치하는 자산이 없습니다.";

  const rows: React.ReactNode[] = [];
  const pushRows = (list: TreeNode[]) => {
    for (const node of list) {
      const open = isExpanded(node);
      rows.push(
        <AssetTreeRow
          key={node.key}
          node={node}
          open={open}
          selected={selected === node.key}
          onToggle={() => toggle(node)}
          onSelect={() => onSelect(selected === node.key ? null : node.key)}
        />,
      );
      if (open && node.children.length > 0) pushRows(node.children);
    }
  };
  pushRows(visible);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="flex items-center gap-1">
        <InputGroup className="flex-1">
          <InputGroupInput
            type="search"
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="자산 필터"
            aria-label="자산 필터"
          />
          <InputGroupAddon>
            <SearchIcon aria-hidden="true" />
          </InputGroupAddon>
        </InputGroup>
        {onRefresh && (
          <Button
            size="icon"
            variant="ghost"
            className="size-8 shrink-0 text-muted-foreground"
            onClick={onRefresh}
            disabled={loading}
            aria-label="자산 트리 새로고침"
            title="자산 트리 새로고침"
          >
            <RefreshCwIcon className={cn("size-4", loading && "animate-spin")} />
          </Button>
        )}
      </div>

      <button
        type="button"
        onClick={() => onSelect(null)}
        className={cn(
          "flex items-center justify-between gap-2 rounded-md px-2 py-1.5 text-left text-sm",
          selected === null ? "bg-accent font-medium" : "hover:bg-accent/50",
        )}
      >
        <span>전체 자산</span>
        <span className="text-xs tabular-nums text-muted-foreground">{findingTotal}</span>
      </button>

      <div className="max-h-[24rem] min-h-0 flex-1 overflow-y-auto pr-2 lg:max-h-[calc(100vh-16rem)]">
        <div className="flex flex-col">
          {rows}
          {rows.length === 0 && <p className="px-2 py-8 text-center text-xs text-muted-foreground">{emptyHint}</p>}
        </div>
      </div>

      {truncated && (
        <p className="px-1 text-xs text-muted-foreground">
          자산이 너무 많아 {(droppedKinds ?? []).map((k) => KIND_LABEL[k as FindingAssetKind] ?? k).join(" / ")}
          계층을 숨겼습니다(카운트는 이미 상위 계층에 포함됨). 필터나 필터 입력란으로 범위를 좁히면 전체 계층을 볼 수 있습니다.
        </p>
      )}
    </div>
  );
}

function AssetTreeRow({
  node,
  open,
  selected,
  onToggle,
  onSelect,
}: {
  node: TreeNode;
  open: boolean;
  selected: boolean;
  onToggle: () => void;
  onSelect: () => void;
}) {
  const Icon = KIND_ICON[node.kind] ?? GlobeIcon;
  const hasChildren = node.children.length > 0;
  return (
    <div
      className={cn(
        "group flex items-center gap-1 rounded-md pr-1 text-sm",
        selected ? "bg-accent" : "hover:bg-accent/50",
      )}
      style={{ paddingLeft: `${node.depth * 10}px` }}
    >
      {hasChildren ? (
        <button
          type="button"
          onClick={onToggle}
          className="flex size-5 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground"
          aria-label={open ? "접기" : "펼치기"}
          aria-expanded={open}
        >
          <ChevronRightIcon className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        </button>
      ) : (
        <span className="size-5 shrink-0" />
      )}
      <button
        type="button"
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-1.5 py-1 text-left"
        title={`${KIND_LABEL[node.kind] ?? node.kind} · ${node.label}`}
      >
        <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className={cn("min-w-0 truncate", selected && "font-medium")}>{node.display}</span>
      </button>
      <span className="flex shrink-0 items-center gap-1 text-xs tabular-nums">
        {node.critical > 0 && (
          <span className="text-rose-600" title={`치명 ${node.critical}`}>
            {node.critical}
          </span>
        )}
        {node.high > 0 && (
          <span className="text-red-500" title={`높음 ${node.high}`}>
            {node.high}
          </span>
        )}
        <span className="text-muted-foreground" title={`발견 사항 총 ${node.total}건`}>
          {node.total}
        </span>
      </span>
    </div>
  );
}
