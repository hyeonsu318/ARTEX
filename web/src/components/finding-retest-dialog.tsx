"use client";

import * as React from "react";

import { RotateCcwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

interface FindingRetestDialogProps {
  findingId: string;
  findingName?: string;
  onClose: () => void;
  onStarted?: (retest: FindingRetest) => void;
}

// 열려 있을 때만 마운트하며, 닫으면 설명을 비운다. 목록과 상세가 제출 잠금 및 오류 처리를 공유하고, 시작 후에는 현재 페이지에 머문다.
export function FindingRetestDialog({ findingId, findingName, onClose, onStarted }: FindingRetestDialogProps) {
  const notesId = React.useId();
  const [notes, setNotes] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);
  const submitLock = React.useRef(false);

  async function start() {
    if (submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    try {
      const result = await api.startFindingRetest(findingId, notes.trim());
      onStarted?.(result.retest);
      onClose();
      toast.success(result.created ? "재테스트가 시작되었습니다. 「재테스트 중」을 눌러 대화를 확인할 수 있습니다" : "이 취약점은 이미 재테스트 중입니다. 기존 대화를 확인할 수 있습니다");
    } catch (e) {
      toast.error(`재테스트 시작 실패: ${(e as Error).message}`);
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !submitLock.current && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>취약점 #{findingId} 재테스트</DialogTitle>
          <DialogDescription className="break-words">
            {findingName ? <span className="mb-2 block">{findingName}</span> : null}
            재테스트 Agent는 원본 증거와 테스트 제약을 읽고 독립된 대화에서 표적 검증을 수행합니다. 재테스트가 성공적으로 완료되어 수정이 확인되면 취약점 상태가 자동으로 「수정됨」으로 바뀌며, 그 밖의 결론은 기존 상태를 유지합니다.
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field data-disabled={submitting}>
            <FieldLabel htmlFor={notesId}>추가 설명 (선택)</FieldLabel>
            <Textarea
              id={notesId}
              value={notes}
              maxLength={4000}
              rows={4}
              disabled={submitting}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="예: 원래 테스트 계정으로 기존 인터페이스를 검증, 수정 버전은 v2."
            />
            <FieldDescription>수정 버전, 테스트 조건 또는 이번 제한 사항을 추가할 수 있습니다.</FieldDescription>
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" disabled={submitting} onClick={onClose}>
            취소
          </Button>
          <Button disabled={submitting} onClick={() => void start()}>
            {submitting ? <Spinner data-icon="inline-start" /> : <RotateCcwIcon data-icon="inline-start" />}
            {submitting ? "생성 중…" : "재테스트 시작"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
