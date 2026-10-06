"use client";

import * as React from "react";

import { RefreshCwIcon, RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { statusMeta, toneClasses } from "@/lib/status";
import type { NotificationChannel, NotificationDelivery } from "@/lib/types";

// DeliveryList는 전송 기록 표이다: 채널과 상태로 필터링할 수 있고, 실패한 항목은 수동으로 재전송할 수 있다.
export function DeliveryList({ channels }: { channels: NotificationChannel[] }) {
  const [rows, setRows] = React.useState<NotificationDelivery[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [channelID, setChannelID] = React.useState<number | undefined>(undefined);
  const [state, setState] = React.useState<string | undefined>(undefined);
  const [loading, setLoading] = React.useState(false);
  const pageSize = 50;

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .notifyDeliveries({ channelId: channelID, state, page, pageSize })
      .then((r) => {
        setRows(r.deliveries);
        setTotal(r.total);
      })
      .catch((e) => toast.error("전송 기록을 읽지 못했습니다: " + (e as Error).message))
      .finally(() => setLoading(false));
  }, [channelID, state, page]);
  React.useEffect(() => {
    load();
  }, [load]);

  async function retry(id: number) {
    try {
      await api.notifyRetryDelivery(id);
      toast.success("다시 대기열에 넣었습니다");
      load();
    } catch (e) {
      toast.error("재전송 실패: " + (e as Error).message);
    }
  }

  const maxPage = Math.max(1, Math.ceil(total / pageSize));

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={channelID ? String(channelID) : "all"}
          onValueChange={(v) => {
            setPage(1);
            setChannelID(v === "all" ? undefined : Number(v));
          }}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder="전체 채널" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">전체 채널</SelectItem>
            {channels.map((c) => (
              <SelectItem key={c.id} value={String(c.id)}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={state ?? "all"}
          onValueChange={(v) => {
            setPage(1);
            setState(v === "all" ? undefined : v);
          }}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder="전체 상태" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">전체 상태</SelectItem>
            {["pending", "sending", "sent", "failed", "skipped"].map((s) => (
              <SelectItem key={s} value={s}>
                {statusMeta("delivery", s).label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant="outline" onClick={load} disabled={loading}>
          <RefreshCwIcon className={loading ? "animate-spin" : ""} /> 새로고침
        </Button>
        <span className="text-muted-foreground ml-auto text-xs">총 {total}건</span>
      </div>

      <Card className="py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-40">시간</TableHead>
              <TableHead>취약점</TableHead>
              <TableHead className="w-40">채널</TableHead>
              <TableHead className="w-24">상태</TableHead>
              <TableHead className="w-16">시도</TableHead>
              <TableHead>오류</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-8 text-center">
                  {loading ? "로딩 중…" : "전송 기록이 없습니다"}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                    {formatTime(d.created_at)}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" className={toneClasses[statusMeta("severity", d.severity).tone]}>
                        {statusMeta("severity", d.severity).label}
                      </Badge>
                      <span className="truncate text-sm">{d.title || "(제목 없음)"}</span>
                      {d.event_kind === "finding_status_changed" && (
                        <Badge variant="outline" className="shrink-0">
                          상태 변경
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm">{d.channel_name}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className={toneClasses[statusMeta("delivery", d.state).tone]}>
                      {statusMeta("delivery", d.state).label}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-sm">{d.attempts}</TableCell>
                  <TableCell className="text-muted-foreground max-w-md text-xs break-all">{d.last_error}</TableCell>
                  <TableCell>
                    {/* 실패/건너뜀 항목에만 재전송 진입점을 제공한다: 이미 전달된 항목을 재전송하면 중복 푸시가 발생한다. */}
                    {(d.state === "failed" || d.state === "skipped") && (
                      <Button size="sm" variant="outline" onClick={() => retry(d.id)}>
                        <RotateCcwIcon /> 재전송
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      {maxPage > 1 && (
        <div className="flex items-center justify-end gap-2">
          <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            이전 페이지
          </Button>
          <span className="text-muted-foreground text-sm">
            {page} / {maxPage}
          </span>
          <Button size="sm" variant="outline" disabled={page >= maxPage} onClick={() => setPage((p) => p + 1)}>
            다음 페이지
          </Button>
        </div>
      )}
    </div>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", { hour12: false });
}
